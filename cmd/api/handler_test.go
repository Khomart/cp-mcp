package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type fakeS3 struct {
	objects map[string]string
	putErr  error
	deleted []string
}

func (f *fakeS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	if f.putErr != nil {
		return nil, f.putErr
	}
	b, _ := io.ReadAll(in.Body)
	f.objects[aws.ToString(in.Key)] = string(b)
	return &s3.PutObjectOutput{}, nil
}

func (f *fakeS3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	body, ok := f.objects[aws.ToString(in.Key)]
	if !ok {
		return nil, errors.New("NoSuchKey")
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader(body))}, nil
}

func (f *fakeS3) DeleteObject(_ context.Context, in *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	f.deleted = append(f.deleted, aws.ToString(in.Key))
	delete(f.objects, aws.ToString(in.Key))
	return &s3.DeleteObjectOutput{}, nil
}

type fakeDDB struct {
	items  []map[string]types.AttributeValue
	putErr error
}

func (f *fakeDDB) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	if f.putErr != nil {
		return nil, f.putErr
	}
	f.items = append(f.items, in.Item)
	return &dynamodb.PutItemOutput{}, nil
}

func (f *fakeDDB) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	want := in.Key["id"].(*types.AttributeValueMemberS).Value
	for _, item := range f.items {
		if item["id"].(*types.AttributeValueMemberS).Value == want {
			return &dynamodb.GetItemOutput{Item: item}, nil
		}
	}
	return &dynamodb.GetItemOutput{}, nil
}

const testKey = "test-api-key"

const testID = "11111111-2222-3333-4444-555555555555"

func newTestApp() (*app, *fakeS3, *fakeDDB) {
	s := &fakeS3{objects: map[string]string{}}
	d := &fakeDDB{}
	return &app{
		s3: s, ddb: d, bucket: "b", table: "t", apiKey: testKey,
		newID: func() string { return testID },
		now:   func() time.Time { return time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC) },
	}, s, d
}

func request(method, path, body string) events.APIGatewayV2HTTPRequest {
	req := events.APIGatewayV2HTTPRequest{
		RawPath: path,
		Body:    body,
		Headers: map[string]string{"x-api-key": testKey},
	}
	req.RequestContext.HTTP.Method = method
	return req
}

func postFiles(body string, b64 bool) events.APIGatewayV2HTTPRequest {
	req := request(http.MethodPost, "/files", body)
	req.IsBase64Encoded = b64
	return req
}

func TestCreateFile(t *testing.T) {
	a, s, d := newTestApp()

	resp, err := a.handle(context.Background(), postFiles("hello world", false))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, resp.Body)
	}
	var out createFileResponse
	if err := json.Unmarshal([]byte(resp.Body), &out); err != nil || out.ID != testID {
		t.Fatalf("body = %s, err = %v", resp.Body, err)
	}

	key := "files/" + testID + ".txt"
	if got := s.objects[key]; got != "hello world" {
		t.Errorf("s3 object = %q", got)
	}
	if len(d.items) != 1 {
		t.Fatalf("dynamo items = %d", len(d.items))
	}
	item := d.items[0]
	if v := item["id"].(*types.AttributeValueMemberS).Value; v != testID {
		t.Errorf("id = %q", v)
	}
	if v := item["file_name"].(*types.AttributeValueMemberS).Value; v != key {
		t.Errorf("file_name = %q", v)
	}
	if v := item["size_bytes"].(*types.AttributeValueMemberN).Value; v != "11" {
		t.Errorf("size_bytes = %q", v)
	}
}

func TestCreateFileBase64Body(t *testing.T) {
	a, s, _ := newTestApp()
	enc := base64.StdEncoding.EncodeToString([]byte("encoded text"))

	resp, _ := a.handle(context.Background(), postFiles(enc, true))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := s.objects["files/"+testID+".txt"]; got != "encoded text" {
		t.Errorf("s3 object = %q", got)
	}
}

func TestCreateFileEmptyBody(t *testing.T) {
	a, s, d := newTestApp()

	resp, _ := a.handle(context.Background(), postFiles("", false))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if len(s.objects) != 0 || len(d.items) != 0 {
		t.Error("nothing should be written for an empty body")
	}
}

func TestCreateFileS3Failure(t *testing.T) {
	a, s, d := newTestApp()
	s.putErr = errors.New("boom")

	resp, _ := a.handle(context.Background(), postFiles("x", false))
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if len(d.items) != 0 {
		t.Error("dynamo must not be written when s3 fails")
	}
}

func TestCreateFileDynamoFailureCleansUpBlob(t *testing.T) {
	a, s, d := newTestApp()
	d.putErr = errors.New("boom")

	resp, _ := a.handle(context.Background(), postFiles("x", false))
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if len(s.objects) != 0 || len(s.deleted) != 1 {
		t.Errorf("orphaned blob not cleaned up: objects=%v deleted=%v", s.objects, s.deleted)
	}
}

func TestUnknownRoute(t *testing.T) {
	a, _, _ := newTestApp()
	resp, _ := a.handle(context.Background(), request(http.MethodGet, "/nope", ""))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestGetFileRoundTrip(t *testing.T) {
	a, _, _ := newTestApp()
	if resp, _ := a.handle(context.Background(), postFiles("stored text", false)); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}

	// Uppercase GUID should still resolve to the stored lowercase id.
	resp, _ := a.handle(context.Background(), request(http.MethodGet, "/files/"+strings.ToUpper(testID), ""))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, resp.Body)
	}
	if resp.Body != "stored text" {
		t.Errorf("body = %q", resp.Body)
	}
	if ct := resp.Headers["content-type"]; !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("content-type = %q", ct)
	}
}

func TestGetFileNotFound(t *testing.T) {
	a, _, _ := newTestApp()
	resp, _ := a.handle(context.Background(), request(http.MethodGet, "/files/"+testID, ""))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestGetFileInvalidID(t *testing.T) {
	a, _, _ := newTestApp()
	resp, _ := a.handle(context.Background(), request(http.MethodGet, "/files/not-a-guid", ""))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestAPIKeyRequired(t *testing.T) {
	cases := map[string]map[string]string{
		"missing": nil,
		"wrong":   {"x-api-key": "nope"},
	}
	for name, headers := range cases {
		t.Run(name, func(t *testing.T) {
			a, s, d := newTestApp()
			for _, req := range []events.APIGatewayV2HTTPRequest{
				postFiles("x", false),
				request(http.MethodGet, "/files/"+testID, ""),
			} {
				req.Headers = headers
				resp, _ := a.handle(context.Background(), req)
				if resp.StatusCode != http.StatusUnauthorized {
					t.Errorf("%s %s: status = %d", req.RequestContext.HTTP.Method, req.RawPath, resp.StatusCode)
				}
			}
			if len(s.objects) != 0 || len(d.items) != 0 {
				t.Error("unauthorized request must not write anything")
			}
		})
	}
}

func TestEmptyConfiguredKeyRejectsEverything(t *testing.T) {
	a, _, _ := newTestApp()
	a.apiKey = ""
	req := postFiles("x", false)
	req.Headers = map[string]string{"x-api-key": ""}
	if resp, _ := a.handle(context.Background(), req); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestHealthIsPublic(t *testing.T) {
	a, _, _ := newTestApp()
	req := request(http.MethodGet, "/health", "")
	req.Headers = nil
	if resp, _ := a.handle(context.Background(), req); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}
