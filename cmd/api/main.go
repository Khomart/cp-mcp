package main

import (
	"context"
	"os"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

func main() {
	ctx := context.Background()

	// Clients are created once per container and reused across invocations.
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		panic(err)
	}

	// The API key is read once per cold start; rotating it takes effect on new containers.
	param, err := ssm.NewFromConfig(cfg).GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(os.Getenv("API_KEY_PARAM")),
		WithDecryption: aws.Bool(true),
	})
	if err != nil {
		panic(err)
	}

	a := &app{
		s3:     s3.NewFromConfig(cfg),
		ddb:    dynamodb.NewFromConfig(cfg),
		bucket: os.Getenv("BUCKET_NAME"),
		table:  os.Getenv("TABLE_NAME"),
		apiKey: aws.ToString(param.Parameter.Value),
		newID:  newUUID,
		now:    time.Now,
	}
	lambda.Start(a.handle)
}
