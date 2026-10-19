package cloudwatchlog

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
)

type fakeAPI struct {
	groups, streams, retentions int
	puts                        [][]string
}

func (f *fakeAPI) CreateLogGroup(context.Context, *cloudwatchlogs.CreateLogGroupInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.CreateLogGroupOutput, error) {
	f.groups++
	return &cloudwatchlogs.CreateLogGroupOutput{}, nil
}

func (f *fakeAPI) CreateLogStream(context.Context, *cloudwatchlogs.CreateLogStreamInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.CreateLogStreamOutput, error) {
	f.streams++
	return &cloudwatchlogs.CreateLogStreamOutput{}, nil
}

func (f *fakeAPI) PutRetentionPolicy(context.Context, *cloudwatchlogs.PutRetentionPolicyInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.PutRetentionPolicyOutput, error) {
	f.retentions++
	return &cloudwatchlogs.PutRetentionPolicyOutput{}, nil
}

func (f *fakeAPI) PutLogEvents(_ context.Context, input *cloudwatchlogs.PutLogEventsInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.PutLogEventsOutput, error) {
	messages := make([]string, len(input.LogEvents))
	for i, event := range input.LogEvents {
		messages[i] = aws.ToString(event.Message)
	}
	f.puts = append(f.puts, messages)
	return &cloudwatchlogs.PutLogEventsOutput{}, nil
}

func TestWriterBatchesCreatesAndFlushes(t *testing.T) {
	client := &fakeAPI{}
	w := newWriter(client, Config{
		Region: "af-south-1", Group: "/monitor/test", StreamTemplate: "{app}-{env}-{hostname}-{date}", Environment: "test",
		CreateGroup: true, BatchSize: 2, BufferSize: 10, RetentionDays: 30, FlushInterval: time.Hour,
	})
	w.hostname = "web-01"
	if _, err := w.Write([]byte("{\"message\":\"first\"}\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("{\"message\":\"second\"}\n")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := w.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if client.groups != 1 || client.streams != 1 || client.retentions != 1 {
		t.Fatalf("destination calls: groups=%d streams=%d retentions=%d", client.groups, client.streams, client.retentions)
	}
	if len(client.puts) != 1 || len(client.puts[0]) != 2 {
		t.Fatalf("put batches: %#v", client.puts)
	}
	if client.puts[0][0] != "{\"message\":\"first\"}" || client.puts[0][1] != "{\"message\":\"second\"}" {
		t.Fatalf("unexpected messages: %#v", client.puts[0])
	}
}

func TestWriterFlushesPartialBatchOnClose(t *testing.T) {
	client := &fakeAPI{}
	w := newWriter(client, Config{
		Region: "af-south-1", Group: "/monitor/test", StreamTemplate: "monitor-test", Environment: "test",
		BatchSize: 25, BufferSize: 100, RetentionDays: 30, FlushInterval: time.Hour,
	})
	_, _ = w.Write([]byte("{\"message\":\"shutdown\"}\n"))
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := w.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if len(client.puts) != 1 || len(client.puts[0]) != 1 {
		t.Fatalf("shutdown batch was not flushed: %#v", client.puts)
	}
	if client.groups != 0 || client.retentions != 0 || client.streams != 1 {
		t.Fatalf("unexpected destination calls without group creation: groups=%d streams=%d retentions=%d", client.groups, client.streams, client.retentions)
	}
}

func TestWriterBoundsRetainedBatch(t *testing.T) {
	w := &Writer{cfg: Config{BufferSize: 2}}
	batch := w.appendEvent(nil, queuedEvent{message: "one"})
	batch = w.appendEvent(batch, queuedEvent{message: "two"})
	batch = w.appendEvent(batch, queuedEvent{message: "three"})
	if len(batch) != 2 {
		t.Fatalf("retained batch length = %d, want 2", len(batch))
	}
	if dropped := w.dropped.Load(); dropped != 1 {
		t.Fatalf("dropped count = %d, want 1", dropped)
	}
}
