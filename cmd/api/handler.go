package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
)

// Narrow interfaces over the AWS clients so the handler can be tested with fakes.
type blobStore interface {
	GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	DeleteObject(ctx context.Context, in *s3.DeleteObjectInput, opts ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

type fileTable interface {
	GetItem(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	PutItem(ctx context.Context, in *dynamodb.PutItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
}

type app struct {
	s3     blobStore
	ddb    fileTable
	bucket string
	table  string
	apiKey string
	newID  func() string
	now    func() time.Time
}

type createFileResponse struct {
	ID string `json:"id"`
}

func jsonResponse(status int, body any) (events.APIGatewayV2HTTPResponse, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return events.APIGatewayV2HTTPResponse{}, err
	}
	return events.APIGatewayV2HTTPResponse{
		StatusCode: status,
		Headers:    map[string]string{"content-type": "application/json"},
		Body:       string(b),
	}, nil
}

func errorResponse(status int, msg string) (events.APIGatewayV2HTTPResponse, error) {
	return jsonResponse(status, map[string]string{"error": msg})
}

func (a *app) handle(ctx context.Context, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	method, path := req.RequestContext.HTTP.Method, strings.TrimSuffix(req.RawPath, "/")

	// Health stays public so uptime checks don't need the key.
	if method == http.MethodGet && path == "/health" {
		return jsonResponse(http.StatusOK, map[string]bool{"ok": true})
	}

	if !a.authorized(req) {
		return errorResponse(http.StatusUnauthorized, "missing or invalid API key")
	}

	switch {
	case method == http.MethodPost && path == "/files":
		return a.createFile(ctx, req)
	case method == http.MethodGet && strings.HasPrefix(path, "/files/"):
		return a.getFile(ctx, strings.TrimPrefix(path, "/files/"))
	}
	return errorResponse(http.StatusNotFound, "route not found")
}

// authorized checks the x-api-key header. HTTP APIs deliver header names lowercased.
func (a *app) authorized(req events.APIGatewayV2HTTPRequest) bool {
	got := req.Headers["x-api-key"]
	if a.apiKey == "" || got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(a.apiKey)) == 1
}

// createFile stores the raw request body as a new S3 object, records it in DynamoDB,
// and returns the generated GUID.
func (a *app) createFile(ctx context.Context, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	body := []byte(req.Body)
	if req.IsBase64Encoded {
		decoded, err := base64.StdEncoding.DecodeString(req.Body)
		if err != nil {
			return errorResponse(http.StatusBadRequest, "invalid base64 body")
		}
		body = decoded
	}
	if len(body) == 0 {
		return errorResponse(http.StatusBadRequest, "request body must contain text")
	}

	id := a.newID()
	fileName := "files/" + id + ".txt"

	// S3 first: a DynamoDB entry must never point at a blob that doesn't exist.
	if _, err := a.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(a.bucket),
		Key:           aws.String(fileName),
		Body:          bytes.NewReader(body),
		ContentLength: aws.Int64(int64(len(body))),
		ContentType:   aws.String("text/plain; charset=utf-8"),
	}); err != nil {
		slog.ErrorContext(ctx, "s3 put failed", "id", id, "err", err)
		return errorResponse(http.StatusInternalServerError, "failed to store file")
	}

	_, err := a.ddb.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(a.table),
		Item: map[string]types.AttributeValue{
			"id":         &types.AttributeValueMemberS{Value: id},
			"file_name":  &types.AttributeValueMemberS{Value: fileName},
			"size_bytes": &types.AttributeValueMemberN{Value: strconv.Itoa(len(body))},
			"created_at": &types.AttributeValueMemberS{Value: a.now().UTC().Format(time.RFC3339)},
		},
		ConditionExpression: aws.String("attribute_not_exists(id)"),
	})
	if err != nil {
		slog.ErrorContext(ctx, "dynamodb put failed", "id", id, "err", err)
		// Best-effort cleanup so we don't leave an orphaned blob behind.
		if _, delErr := a.s3.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(a.bucket),
			Key:    aws.String(fileName),
		}); delErr != nil {
			slog.ErrorContext(ctx, "s3 cleanup failed", "id", id, "err", delErr)
		}
		var condErr *types.ConditionalCheckFailedException
		if errors.As(err, &condErr) {
			return errorResponse(http.StatusConflict, "id collision, retry the request")
		}
		return errorResponse(http.StatusInternalServerError, "failed to record file")
	}

	return jsonResponse(http.StatusCreated, createFileResponse{ID: id})
}

func newUUID() string { return uuid.NewString() }

// getFile looks up the GUID in DynamoDB and returns the stored text from S3.
func (a *app) getFile(ctx context.Context, rawID string) (events.APIGatewayV2HTTPResponse, error) {
	parsed, err := uuid.Parse(rawID)
	if err != nil {
		return errorResponse(http.StatusBadRequest, "id must be a GUID")
	}
	id := parsed.String() // canonical lowercase form, as stored by createFile

	out, err := a.ddb.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(a.table),
		Key:       map[string]types.AttributeValue{"id": &types.AttributeValueMemberS{Value: id}},
	})
	if err != nil {
		slog.ErrorContext(ctx, "dynamodb get failed", "id", id, "err", err)
		return errorResponse(http.StatusInternalServerError, "failed to look up file")
	}
	if out.Item == nil {
		return errorResponse(http.StatusNotFound, "file not found")
	}
	fileName, ok := out.Item["file_name"].(*types.AttributeValueMemberS)
	if !ok {
		slog.ErrorContext(ctx, "item has no file_name", "id", id)
		return errorResponse(http.StatusInternalServerError, "file record is corrupt")
	}

	obj, err := a.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(a.bucket),
		Key:    aws.String(fileName.Value),
	})
	if err != nil {
		slog.ErrorContext(ctx, "s3 get failed", "id", id, "key", fileName.Value, "err", err)
		return errorResponse(http.StatusInternalServerError, "failed to read file")
	}
	defer obj.Body.Close()
	body, err := io.ReadAll(obj.Body)
	if err != nil {
		slog.ErrorContext(ctx, "s3 read failed", "id", id, "err", err)
		return errorResponse(http.StatusInternalServerError, "failed to read file")
	}

	return events.APIGatewayV2HTTPResponse{
		StatusCode: http.StatusOK,
		Headers:    map[string]string{"content-type": "text/plain; charset=utf-8"},
		Body:       string(body),
	}, nil
}
