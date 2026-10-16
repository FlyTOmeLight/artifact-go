//go:build ignore

// This acceptance helper receives temporary credentials only through stdin.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	api "github.com/FlyTOmeLight/artifact-go/sdk/go/storagev1"
)

func run() error {
	var input struct {
		Operation string
		Grant     api.GrantResponse
		Data      string
	}
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 65536)).Decode(&input); err != nil {
		return err
	}
	if len(input.Grant.Files) != 1 {
		return errors.New("one file required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := s3.New(s3.Options{
		Region:           input.Grant.Region,
		BaseEndpoint:     aws.String(input.Grant.Endpoint),
		UsePathStyle:     input.Grant.ForcePathStyle,
		RetryMaxAttempts: 1,
		HTTPClient:       &http.Client{Timeout: 20 * time.Second},
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{
				AccessKeyID:     input.Grant.Credentials.AccessKeyID,
				SecretAccessKey: input.Grant.Credentials.SecretAccessKey,
				SessionToken:    input.Grant.Credentials.SessionToken,
			}, nil
		}),
	})
	file := input.Grant.Files[0]
	switch input.Operation {
	case "put":
		_, err := client.PutObject(
			ctx,
			&s3.PutObjectInput{
				Bucket: aws.String(file.Bucket),
				Key:    aws.String(file.ObjectKey),
				Body:   bytes.NewReader([]byte(input.Data)),
			},
		)
		return err
	case "get":
		result, err := client.GetObject(
			ctx,
			&s3.GetObjectInput{Bucket: aws.String(file.Bucket), Key: aws.String(file.ObjectKey)},
		)
		if err != nil {
			return err
		}
		defer result.Body.Close()
		data, err := io.ReadAll(io.LimitReader(result.Body, int64(len(input.Data)+1)))
		if err != nil {
			return err
		}
		if !bytes.Equal(data, []byte(input.Data)) {
			return errors.New("content mismatch")
		}
		return nil
	default:
		return errors.New("invalid operation")
	}
}

func main() {
	if run() != nil {
		fmt.Println(`{"status":"failed"}`)
		os.Exit(1)
	}
	fmt.Println(`{"status":"passed"}`)
}
