// Package awsclients provides thin, nil-safe wrappers over the AWS SDK for the
// two cloud services go-you (re)introduces on the /v1/persona path: DynamoDB
// (the OrganicData + EmailPhoneMeta caches) and Kinesis (the analytics sink).
//
// These mirror the Python common-utils CustomDynamoDBClient / KinesisClient:
//   - DynamoDB items use a single string partition key named "id" (no sort key).
//   - Region + credentials come from the SDK default chain (AWS_REGION /
//     AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY / IAM role), exactly like boto3.
//
// Every constructor is best-effort: a config-load failure logs and returns nil,
// so the caller degrades to the no-cache / no-analytics behavior rather than
// failing to boot — matching the optional-dependency pattern used by
// internal/staticdata.
package awsclients

import (
	"context"
	"log"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/kinesis"
)

// loadConfig loads the shared AWS config once (default credential chain +
// region). Returned to both clients so credentials are resolved a single time.
func loadConfig(ctx context.Context) (awssdk.Config, bool) {
	cfg, err := awscfg.LoadDefaultConfig(ctx)
	if err != nil {
		log.Printf("awsclients: LoadDefaultConfig failed (AWS services disabled): %v", err)
		return awssdk.Config{}, false
	}
	return cfg, true
}

// DynamoClient wraps a dynamodb.Client for the get/put-by-id access the caches
// need. A nil *DynamoClient is a safe no-op (Get returns not-found, Put no-ops),
// so callers never have to nil-check before every call.
type DynamoClient struct {
	api *dynamodb.Client
}

// NewDynamo builds a DynamoClient from the default AWS config. Returns nil when
// the config cannot be loaded (best-effort). An optional endpoint override
// (for DynamoDB Local / LocalStack in tests) is read from AWS_ENDPOINT_URL by
// the SDK automatically, so nothing extra is needed here.
func NewDynamo(ctx context.Context) *DynamoClient {
	cfg, ok := loadConfig(ctx)
	if !ok {
		return nil
	}
	return &DynamoClient{api: dynamodb.NewFromConfig(cfg)}
}

// GetItem fetches the item with partition key id from table. It returns
// (nil, nil) when the item is absent — a miss is not an error. A nil client
// returns (nil, nil) too.
func (c *DynamoClient) GetItem(ctx context.Context, table, id string) (map[string]ddbtypes.AttributeValue, error) {
	if c == nil || c.api == nil || table == "" || id == "" {
		return nil, nil
	}
	out, err := c.api.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: awssdk.String(table),
		Key:       map[string]ddbtypes.AttributeValue{"id": &ddbtypes.AttributeValueMemberS{Value: id}},
	})
	if err != nil {
		return nil, err
	}
	if len(out.Item) == 0 {
		return nil, nil // miss
	}
	return out.Item, nil
}

// PutItem writes item (which must include the "id" attribute) to table. A nil
// client no-ops.
func (c *DynamoClient) PutItem(ctx context.Context, table string, item map[string]ddbtypes.AttributeValue) error {
	if c == nil || c.api == nil || table == "" {
		return nil
	}
	_, err := c.api.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: awssdk.String(table),
		Item:      item,
	})
	return err
}

// KinesisClient wraps a kinesis.Client for a single PutRecord. A nil
// *KinesisClient is a safe no-op.
type KinesisClient struct {
	api *kinesis.Client
}

// NewKinesis builds a KinesisClient from the default AWS config. Returns nil on
// config-load failure (best-effort).
func NewKinesis(ctx context.Context) *KinesisClient {
	cfg, ok := loadConfig(ctx)
	if !ok {
		return nil
	}
	return &KinesisClient{api: kinesis.NewFromConfig(cfg)}
}

// Publish sends data to stream under partitionKey (tenant id), mirroring the
// Python KinesisClient.publish (Data = raw UTF-8 JSON bytes, no compression).
// A nil client no-ops.
func (c *KinesisClient) Publish(ctx context.Context, stream string, data []byte, partitionKey string) error {
	if c == nil || c.api == nil || stream == "" {
		return nil
	}
	_, err := c.api.PutRecord(ctx, &kinesis.PutRecordInput{
		StreamName:   awssdk.String(stream),
		Data:         data,
		PartitionKey: awssdk.String(partitionKey),
	})
	return err
}
