package cloudwatchlog

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
)

const (
	maxBatchBytes = 1_048_576
	maxEventBytes = 256 * 1024
)

type Config struct {
	Region, Group, StreamTemplate, Environment string
	CreateGroup                                bool
	BatchSize, BufferSize, RetentionDays       int
	FlushInterval                              time.Duration
}

type api interface {
	CreateLogGroup(context.Context, *cloudwatchlogs.CreateLogGroupInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.CreateLogGroupOutput, error)
	CreateLogStream(context.Context, *cloudwatchlogs.CreateLogStreamInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.CreateLogStreamOutput, error)
	PutLogEvents(context.Context, *cloudwatchlogs.PutLogEventsInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.PutLogEventsOutput, error)
	PutRetentionPolicy(context.Context, *cloudwatchlogs.PutRetentionPolicyInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.PutRetentionPolicyOutput, error)
}

type queuedEvent struct {
	message   string
	timestamp int64
}

type closeRequest struct {
	ctx    context.Context
	result chan error
}

type Writer struct {
	client          api
	cfg             Config
	hostname        string
	events          chan queuedEvent
	closeCh         chan closeRequest
	done            chan struct{}
	closed          atomic.Bool
	dropped         atomic.Uint64
	droppedOversize atomic.Uint64
	droppedBuffer   atomic.Uint64
	closeMu         sync.Mutex
	closeErr        error
}

func New(ctx context.Context, cfg Config) (*Writer, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.Region))
	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}
	return newWriter(cloudwatchlogs.NewFromConfig(awsCfg), cfg), nil
}

func newWriter(client api, cfg Config) *Writer {
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "unknown"
	}
	hostname = strings.NewReplacer(":", "-", "*", "-").Replace(hostname)
	w := &Writer{
		client:   client,
		cfg:      cfg,
		hostname: hostname,
		events:   make(chan queuedEvent, cfg.BufferSize),
		closeCh:  make(chan closeRequest),
		done:     make(chan struct{}),
	}
	go w.run()
	return w
}

func (w *Writer) Write(p []byte) (int, error) {
	if w.closed.Load() {
		return len(p), nil
	}
	message := bytes.TrimSuffix(p, []byte{'\n'})
	if len(message) == 0 {
		return len(p), nil
	}
	if len(message) > maxEventBytes {
		w.dropped.Add(1)
		w.droppedOversize.Add(1)
		return len(p), nil
	}
	event := queuedEvent{message: string(bytes.Clone(message)), timestamp: time.Now().UnixMilli()}
	select {
	case w.events <- event:
	default:
		w.dropped.Add(1)
		w.droppedBuffer.Add(1)
	}
	return len(p), nil
}

func (w *Writer) Close(ctx context.Context) error {
	if !w.closed.CompareAndSwap(false, true) {
		select {
		case <-w.done:
			w.closeMu.Lock()
			defer w.closeMu.Unlock()
			return w.closeErr
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	request := closeRequest{ctx: ctx, result: make(chan error, 1)}
	select {
	case w.closeCh <- request:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-request.result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *Writer) run() {
	defer close(w.done)
	ticker := time.NewTicker(w.cfg.FlushInterval)
	defer ticker.Stop()
	batch := make([]queuedEvent, 0, w.cfg.BatchSize)
	groupReady := false
	streamReady := make(map[string]bool)
	deliveryFailed := false
	for {
		select {
		case event := <-w.events:
			batch = w.appendEvent(batch, event)
			if len(batch) >= w.cfg.BatchSize && !deliveryFailed {
				batch, deliveryFailed = w.tryFlush(batch, &groupReady, streamReady)
			}
		case <-ticker.C:
			batch, deliveryFailed = w.tryFlush(batch, &groupReady, streamReady)
		case request := <-w.closeCh:
			for {
				select {
				case event := <-w.events:
					batch = w.appendEvent(batch, event)
				default:
					_, err := w.flush(request.ctx, batch, &groupReady, streamReady)
					if err != nil {
						w.diagnostic("close_flush_events", "CloudWatch log delivery failed during shutdown", err, "batch_events", len(batch))
					}
					if dropped := w.dropped.Load(); dropped > 0 {
						w.diagnostic("close_dropped_events", "CloudWatch log events dropped", fmt.Errorf("count=%d", dropped), "dropped_events", dropped, "dropped_oversize", w.droppedOversize.Load(), "dropped_buffer_full", w.droppedBuffer.Load())
					}
					w.closeMu.Lock()
					w.closeErr = err
					w.closeMu.Unlock()
					request.result <- err
					return
				}
			}
		}
	}
}

func (w *Writer) appendEvent(batch []queuedEvent, event queuedEvent) []queuedEvent {
	if len(batch) >= w.cfg.BufferSize {
		w.dropped.Add(1)
		w.droppedBuffer.Add(1)
		return batch
	}
	return append(batch, event)
}

func (w *Writer) tryFlush(batch []queuedEvent, groupReady *bool, streamReady map[string]bool) ([]queuedEvent, bool) {
	if len(batch) == 0 {
		return batch, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sent, err := w.flush(ctx, batch, groupReady, streamReady)
	if err != nil {
		w.diagnostic("flush_events", "CloudWatch log delivery failed", err, "batch_events", len(batch), "sent_events", sent)
		return batch[sent:], true
	}
	return batch[:0], false
}

func (w *Writer) flush(ctx context.Context, batch []queuedEvent, groupReady *bool, streamReady map[string]bool) (int, error) {
	if len(batch) == 0 {
		return 0, nil
	}
	stream := w.streamName(time.Now().UTC())
	if err := w.ensureDestination(ctx, stream, groupReady, streamReady); err != nil {
		return 0, err
	}
	slices.SortStableFunc(batch, func(a, b queuedEvent) int { return cmp.Compare(a.timestamp, b.timestamp) })
	sent := 0
	for len(batch) > 0 {
		count, size := 0, 0
		for count < len(batch) && count < 10000 {
			eventSize := len(batch[count].message) + 26
			if count > 0 && size+eventSize > maxBatchBytes {
				break
			}
			size += eventSize
			count++
		}
		events := make([]types.InputLogEvent, count)
		for i, event := range batch[:count] {
			events[i] = types.InputLogEvent{Message: aws.String(event.message), Timestamp: aws.Int64(event.timestamp)}
		}
		output, err := w.client.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{LogEvents: events, LogGroupName: aws.String(w.cfg.Group), LogStreamName: aws.String(stream)})
		if err != nil && isNotFound(err) && w.cfg.CreateGroup {
			*groupReady = false
			delete(streamReady, stream)
			if ensureErr := w.ensureDestination(ctx, stream, groupReady, streamReady); ensureErr != nil {
				return sent, ensureErr
			}
			output, err = w.client.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{LogEvents: events, LogGroupName: aws.String(w.cfg.Group), LogStreamName: aws.String(stream)})
		}
		if err != nil {
			return sent, err
		}
		if output.RejectedLogEventsInfo != nil {
			return sent + count, fmt.Errorf("CloudWatch rejected log events: too_old=%d too_new=%d expired=%d", aws.ToInt32(output.RejectedLogEventsInfo.TooOldLogEventEndIndex), aws.ToInt32(output.RejectedLogEventsInfo.TooNewLogEventStartIndex), aws.ToInt32(output.RejectedLogEventsInfo.ExpiredLogEventEndIndex))
		}
		sent += count
		batch = batch[count:]
	}
	return sent, nil
}

func (w *Writer) ensureDestination(ctx context.Context, stream string, groupReady *bool, streamReady map[string]bool) error {
	if !*groupReady {
		created := false
		if w.cfg.CreateGroup {
			_, err := w.client.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(w.cfg.Group)})
			if err != nil && !isAlreadyExists(err) {
				return fmt.Errorf("create log group %q: %w", w.cfg.Group, err)
			}
			created = err == nil
		}
		if created {
			_, err := w.client.PutRetentionPolicy(ctx, &cloudwatchlogs.PutRetentionPolicyInput{LogGroupName: aws.String(w.cfg.Group), RetentionInDays: aws.Int32(int32(w.cfg.RetentionDays))})
			if err != nil {
				return fmt.Errorf("set log group retention group=%q retention_days=%d: %w", w.cfg.Group, w.cfg.RetentionDays, err)
			}
		}
		*groupReady = true
	}
	if streamReady[stream] {
		return nil
	}
	_, err := w.client.CreateLogStream(ctx, &cloudwatchlogs.CreateLogStreamInput{LogGroupName: aws.String(w.cfg.Group), LogStreamName: aws.String(stream)})
	if err != nil && !isAlreadyExists(err) {
		return fmt.Errorf("create log stream group=%q stream=%q: %w", w.cfg.Group, stream, err)
	}
	streamReady[stream] = true
	return nil
}

func (w *Writer) streamName(now time.Time) string {
	return strings.NewReplacer(
		"{app}", "monitor",
		"{env}", w.cfg.Environment,
		"{hostname}", w.hostname,
		"{date}", now.Format("2006-01-02"),
	).Replace(w.cfg.StreamTemplate)
}

func isAlreadyExists(err error) bool {
	_, ok := errors.AsType[*types.ResourceAlreadyExistsException](err)
	return ok
}

func isNotFound(err error) bool {
	_, ok := errors.AsType[*types.ResourceNotFoundException](err)
	return ok
}

func (w *Writer) diagnostic(operation, message string, err error, attrs ...any) {
	record := map[string]any{
		"timestamp":          time.Now().UTC().Format(time.RFC3339Nano),
		"level":              "ERROR",
		"service":            "monitor",
		"log_schema_version": 1,
		"component":          "cloudwatch_forwarder",
		"operation":          operation,
		"source":             "internal/cloudwatchlog/writer.go",
		"message":            message,
		"region":             w.cfg.Region,
		"log_group":          w.cfg.Group,
		"log_stream":         w.streamName(time.Now().UTC()),
		"environment":        w.cfg.Environment,
		"hostname":           w.hostname,
	}
	if err != nil {
		record["error"] = err.Error()
		record["error_type"] = fmt.Sprintf("%T", err)
	}
	for i := 0; i+1 < len(attrs); i += 2 {
		key, ok := attrs[i].(string)
		if ok && key != "error" && key != "error_type" {
			record[key] = attrs[i+1]
		}
	}
	if raw, marshalErr := json.Marshal(record); marshalErr == nil {
		_, _ = os.Stderr.Write(append(raw, '\n'))
	}
}
