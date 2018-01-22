package broker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"reflect"
	"testing"

	"github.com/davecgh/go-spew/spew"
	"github.com/hashicorp/consul/testutil/retry"
	"github.com/hashicorp/raft"
	"github.com/stretchr/testify/require"

	"github.com/travisjeffery/jocko"
	"github.com/travisjeffery/jocko/log"
	"github.com/travisjeffery/jocko/mock"
	"github.com/travisjeffery/jocko/protocol"
	"github.com/travisjeffery/jocko/testutil"
)

func TestBroker_Run(t *testing.T) {
	// creating the config up here so we can set the nodeid in the expected test cases
	dir, config := testutil.TestConfig(t)
	config.Bootstrap = true
	config.BootstrapExpect = 1
	config.StartAsLeader = true
	defer os.RemoveAll(dir)
	mustEncode := func(e protocol.Encoder) []byte {
		var b []byte
		var err error
		if b, err = protocol.Encode(e); err != nil {
			panic(err)
		}
		return b
	}
	type args struct {
		requestCh  chan jocko.Request
		responseCh chan jocko.Response
		requests   []jocko.Request
		responses  []jocko.Response
	}
	tests := []struct {
		name      string
		fields    fields
		setFields func(f *fields)
		args      args
	}{
		{
			name:   "api versions",
			fields: newFields(),
			args: args{
				requestCh:  make(chan jocko.Request, 2),
				responseCh: make(chan jocko.Response, 2),
				requests: []jocko.Request{{
					Header:  &protocol.RequestHeader{CorrelationID: 1},
					Request: &protocol.APIVersionsRequest{},
				}},
				responses: []jocko.Response{{
					Header:   &protocol.RequestHeader{CorrelationID: 1},
					Response: &protocol.Response{CorrelationID: 1, Body: APIVersions},
				}},
			},
		},
		{
			name:   "create topic ok",
			fields: newFields(),
			args: args{
				requestCh:  make(chan jocko.Request, 2),
				responseCh: make(chan jocko.Response, 2),
				requests: []jocko.Request{{
					Header: &protocol.RequestHeader{CorrelationID: 1},
					Request: &protocol.CreateTopicRequests{Requests: []*protocol.CreateTopicRequest{{
						Topic:             "the-topic",
						NumPartitions:     1,
						ReplicationFactor: 1,
					}}}},
				},
				responses: []jocko.Response{{
					Header: &protocol.RequestHeader{CorrelationID: 1},
					Response: &protocol.Response{CorrelationID: 1, Body: &protocol.CreateTopicsResponse{
						TopicErrorCodes: []*protocol.TopicErrorCode{{Topic: "the-topic", ErrorCode: protocol.ErrNone.Code()}},
					}},
				}},
			},
		},
		{
			name:   "create topic invalid replication factor error",
			fields: newFields(),
			args: args{
				requestCh:  make(chan jocko.Request, 2),
				responseCh: make(chan jocko.Response, 2),
				requests: []jocko.Request{{
					Header: &protocol.RequestHeader{CorrelationID: 1},
					Request: &protocol.CreateTopicRequests{Requests: []*protocol.CreateTopicRequest{{
						Topic:             "the-topic",
						NumPartitions:     1,
						ReplicationFactor: 2,
					}}}},
				},
				responses: []jocko.Response{{
					Header: &protocol.RequestHeader{CorrelationID: 1},
					Response: &protocol.Response{CorrelationID: 1, Body: &protocol.CreateTopicsResponse{
						TopicErrorCodes: []*protocol.TopicErrorCode{{Topic: "the-topic", ErrorCode: protocol.ErrInvalidReplicationFactor.Code()}},
					}},
				}},
			},
		},
		{
			name:   "delete topic",
			fields: newFields(),
			args: args{
				requestCh:  make(chan jocko.Request, 2),
				responseCh: make(chan jocko.Response, 2),
				requests: []jocko.Request{{
					Header: &protocol.RequestHeader{CorrelationID: 1},
					Request: &protocol.CreateTopicRequests{Requests: []*protocol.CreateTopicRequest{{
						Topic:             "the-topic",
						NumPartitions:     1,
						ReplicationFactor: 1,
					}}}}, {
					Header:  &protocol.RequestHeader{CorrelationID: 2},
					Request: &protocol.DeleteTopicsRequest{Topics: []string{"the-topic"}}},
				},
				responses: []jocko.Response{{
					Header: &protocol.RequestHeader{CorrelationID: 1},
					Response: &protocol.Response{CorrelationID: 1, Body: &protocol.CreateTopicsResponse{
						TopicErrorCodes: []*protocol.TopicErrorCode{{Topic: "the-topic", ErrorCode: protocol.ErrNone.Code()}},
					}},
				}, {
					Header: &protocol.RequestHeader{CorrelationID: 2},
					Response: &protocol.Response{CorrelationID: 2, Body: &protocol.DeleteTopicsResponse{
						TopicErrorCodes: []*protocol.TopicErrorCode{{Topic: "the-topic", ErrorCode: protocol.ErrNone.Code()}},
					}}}},
			},
		},
		{
			name:   "offsets",
			fields: newFields(),
			args: args{
				requestCh:  make(chan jocko.Request, 2),
				responseCh: make(chan jocko.Response, 2),
				requests: []jocko.Request{
					{
						Header: &protocol.RequestHeader{CorrelationID: 1},
						Request: &protocol.CreateTopicRequests{Requests: []*protocol.CreateTopicRequest{{
							Topic:             "the-topic",
							NumPartitions:     1,
							ReplicationFactor: 1,
						}}},
					},
					{
						Header: &protocol.RequestHeader{CorrelationID: 2},
						Request: &protocol.ProduceRequest{TopicData: []*protocol.TopicData{{
							Topic: "the-topic",
							Data: []*protocol.Data{{
								RecordSet: mustEncode(&protocol.MessageSet{Offset: 0, Messages: []*protocol.Message{{Value: []byte("The message.")}}})}}}}},
					},
					{
						Header:  &protocol.RequestHeader{CorrelationID: 3},
						Request: &protocol.OffsetsRequest{ReplicaID: 0, Topics: []*protocol.OffsetsTopic{{Topic: "the-topic", Partitions: []*protocol.OffsetsPartition{{Partition: 0, Timestamp: -1}}}}},
					},
					{
						Header:  &protocol.RequestHeader{CorrelationID: 4},
						Request: &protocol.OffsetsRequest{ReplicaID: 0, Topics: []*protocol.OffsetsTopic{{Topic: "the-topic", Partitions: []*protocol.OffsetsPartition{{Partition: 0, Timestamp: -2}}}}},
					},
				},
				responses: []jocko.Response{
					{
						Header: &protocol.RequestHeader{CorrelationID: 1},
						Response: &protocol.Response{CorrelationID: 1, Body: &protocol.CreateTopicsResponse{
							TopicErrorCodes: []*protocol.TopicErrorCode{{Topic: "the-topic", ErrorCode: protocol.ErrNone.Code()}},
						}},
					},
					{
						Header: &protocol.RequestHeader{CorrelationID: 2},
						Response: &protocol.Response{CorrelationID: 2, Body: &protocol.ProduceResponses{
							Responses: []*protocol.ProduceResponse{{
								Topic:              "the-topic",
								PartitionResponses: []*protocol.ProducePartitionResponse{{Partition: 0, BaseOffset: 0, ErrorCode: protocol.ErrNone.Code()}},
							}},
						}},
					},
					{
						Header: &protocol.RequestHeader{CorrelationID: 3},
						Response: &protocol.Response{CorrelationID: 3, Body: &protocol.OffsetsResponse{
							Responses: []*protocol.OffsetResponse{{
								Topic:              "the-topic",
								PartitionResponses: []*protocol.PartitionResponse{{Partition: 0, Offsets: []int64{1}, ErrorCode: protocol.ErrNone.Code()}},
							}},
						}},
					},
					{
						Header: &protocol.RequestHeader{CorrelationID: 4},
						Response: &protocol.Response{CorrelationID: 4, Body: &protocol.OffsetsResponse{
							Responses: []*protocol.OffsetResponse{{
								Topic:              "the-topic",
								PartitionResponses: []*protocol.PartitionResponse{{Partition: 0, Offsets: []int64{0}, ErrorCode: protocol.ErrNone.Code()}},
							}},
						}},
					},
				},
			},
		},
		{
			name:   "fetch",
			fields: newFields(),
			args: args{
				requestCh:  make(chan jocko.Request, 2),
				responseCh: make(chan jocko.Response, 2),
				requests: []jocko.Request{
					{
						Header: &protocol.RequestHeader{CorrelationID: 1},
						Request: &protocol.CreateTopicRequests{Requests: []*protocol.CreateTopicRequest{{
							Topic:             "the-topic",
							NumPartitions:     1,
							ReplicationFactor: 1,
						}}},
					},
					{
						Header: &protocol.RequestHeader{CorrelationID: 2},
						Request: &protocol.ProduceRequest{TopicData: []*protocol.TopicData{{
							Topic: "the-topic",
							Data: []*protocol.Data{{
								RecordSet: mustEncode(&protocol.MessageSet{Offset: 0, Messages: []*protocol.Message{{Value: []byte("The message.")}}})}}}}},
					},
					{
						Header:  &protocol.RequestHeader{CorrelationID: 3},
						Request: &protocol.FetchRequest{ReplicaID: 1, MinBytes: 5, Topics: []*protocol.FetchTopic{{Topic: "the-topic", Partitions: []*protocol.FetchPartition{{Partition: 0, FetchOffset: 0, MaxBytes: 100}}}}},
					},
				},
				responses: []jocko.Response{
					{
						Header: &protocol.RequestHeader{CorrelationID: 1},
						Response: &protocol.Response{CorrelationID: 1, Body: &protocol.CreateTopicsResponse{
							TopicErrorCodes: []*protocol.TopicErrorCode{{Topic: "the-topic", ErrorCode: protocol.ErrNone.Code()}},
						}},
					},
					{
						Header: &protocol.RequestHeader{CorrelationID: 2},
						Response: &protocol.Response{CorrelationID: 2, Body: &protocol.ProduceResponses{
							Responses: []*protocol.ProduceResponse{
								{
									Topic:              "the-topic",
									PartitionResponses: []*protocol.ProducePartitionResponse{{Partition: 0, BaseOffset: 0, ErrorCode: protocol.ErrNone.Code()}},
								},
							},
						}},
					},
					{
						Header: &protocol.RequestHeader{CorrelationID: 3},
						Response: &protocol.Response{CorrelationID: 3, Body: &protocol.FetchResponses{
							Responses: []*protocol.FetchResponse{{
								Topic: "the-topic",
								PartitionResponses: []*protocol.FetchPartitionResponse{{
									Partition:     0,
									ErrorCode:     protocol.ErrNone.Code(),
									HighWatermark: 1,
									RecordSet:     mustEncode(&protocol.MessageSet{Offset: 0, Messages: []*protocol.Message{{Value: []byte("The message.")}}}),
								}},
							}}},
						},
					},
				},
			},
		},
		{
			name:   "metadata",
			fields: newFields(),
			args: args{
				requestCh:  make(chan jocko.Request, 2),
				responseCh: make(chan jocko.Response, 2),
				requests: []jocko.Request{
					{
						Header: &protocol.RequestHeader{CorrelationID: 1},
						Request: &protocol.CreateTopicRequests{Requests: []*protocol.CreateTopicRequest{{
							Topic:             "the-topic",
							NumPartitions:     1,
							ReplicationFactor: 1,
						}}},
					},
					{
						Header: &protocol.RequestHeader{CorrelationID: 2},
						Request: &protocol.ProduceRequest{TopicData: []*protocol.TopicData{{
							Topic: "the-topic",
							Data: []*protocol.Data{{
								RecordSet: mustEncode(&protocol.MessageSet{Offset: 0, Messages: []*protocol.Message{{Value: []byte("The message.")}}})}}}}},
					},
					{
						Header:  &protocol.RequestHeader{CorrelationID: 3},
						Request: &protocol.MetadataRequest{Topics: []string{"the-topic", "unknown-topic"}},
					},
				},
				responses: []jocko.Response{
					{
						Header: &protocol.RequestHeader{CorrelationID: 1},
						Response: &protocol.Response{CorrelationID: 1, Body: &protocol.CreateTopicsResponse{
							TopicErrorCodes: []*protocol.TopicErrorCode{{Topic: "the-topic", ErrorCode: protocol.ErrNone.Code()}},
						}},
					},
					{
						Header: &protocol.RequestHeader{CorrelationID: 2},
						Response: &protocol.Response{CorrelationID: 2, Body: &protocol.ProduceResponses{
							Responses: []*protocol.ProduceResponse{
								{
									Topic:              "the-topic",
									PartitionResponses: []*protocol.ProducePartitionResponse{{Partition: 0, BaseOffset: 0, ErrorCode: protocol.ErrNone.Code()}},
								},
							},
						}},
					},
					{
						Header: &protocol.RequestHeader{CorrelationID: 3},
						Response: &protocol.Response{CorrelationID: 3, Body: &protocol.MetadataResponse{
							Brokers: []*protocol.Broker{{NodeID: config.ID, Host: "localhost", Port: 9092}},
							TopicMetadata: []*protocol.TopicMetadata{
								{Topic: "the-topic", TopicErrorCode: protocol.ErrNone.Code(), PartitionMetadata: []*protocol.PartitionMetadata{{PartitionErrorCode: protocol.ErrNone.Code(), ParititionID: 0, Leader: 1, Replicas: []int32{1}, ISR: []int32{1}}}},
								{Topic: "unknown-topic", TopicErrorCode: protocol.ErrUnknownTopicOrPartition.Code()},
							},
						}},
					},
				},
			},
		},
		{
			name:   "produce topic/partition doesn't exist error",
			fields: newFields(),
			args: args{
				requestCh:  make(chan jocko.Request, 2),
				responseCh: make(chan jocko.Response, 2),
				requests: []jocko.Request{{
					Header: &protocol.RequestHeader{CorrelationID: 2},
					Request: &protocol.ProduceRequest{TopicData: []*protocol.TopicData{{
						Topic: "another-topic",
						Data: []*protocol.Data{{
							RecordSet: mustEncode(&protocol.MessageSet{Offset: 1, Messages: []*protocol.Message{{Value: []byte("The message.")}}})}}}}}},
				},
				responses: []jocko.Response{{
					Header: &protocol.RequestHeader{CorrelationID: 2},
					Response: &protocol.Response{CorrelationID: 2, Body: &protocol.ProduceResponses{
						Responses: []*protocol.ProduceResponse{{
							Topic:              "another-topic",
							PartitionResponses: []*protocol.ProducePartitionResponse{{Partition: 0, ErrorCode: protocol.ErrUnknownTopicOrPartition.Code()}},
						}},
					}}}},
			},
		},
		{
			name:   "leader and isr leader new partition",
			fields: newFields(),
			args: args{
				requestCh:  make(chan jocko.Request, 2),
				responseCh: make(chan jocko.Response, 2),
				requests: []jocko.Request{{
					Header: &protocol.RequestHeader{CorrelationID: 2},
					Request: &protocol.LeaderAndISRRequest{
						PartitionStates: []*protocol.PartitionState{
							{
								Topic:     "the-topic",
								Partition: 1,
								ISR:       []int32{1},
								Replicas:  []int32{1},
								Leader:    1,
								ZKVersion: 1,
							},
						},
					}},
				},
				responses: []jocko.Response{{
					Header: &protocol.RequestHeader{CorrelationID: 2},
					Response: &protocol.Response{CorrelationID: 2, Body: &protocol.LeaderAndISRResponse{
						Partitions: []*protocol.LeaderAndISRPartition{
							{
								ErrorCode: protocol.ErrNone.Code(),
								Partition: 1,
								Topic:     "the-topic",
							},
						},
					}}}},
			},
		},
		{
			name:   "leader and isr leader become leader",
			fields: newFields(),
			setFields: func(f *fields) {
				f.topicMap = map[string][]*jocko.Partition{
					"the-topic": []*jocko.Partition{{
						Topic:                   "the-topic",
						ID:                      1,
						Replicas:                nil,
						ISR:                     nil,
						Leader:                  0,
						PreferredLeader:         0,
						LeaderAndISRVersionInZK: 0,
					}},
				}
			},
			args: args{
				requestCh:  make(chan jocko.Request, 2),
				responseCh: make(chan jocko.Response, 2),
				requests: []jocko.Request{{
					Header: &protocol.RequestHeader{CorrelationID: 2},
					Request: &protocol.LeaderAndISRRequest{
						PartitionStates: []*protocol.PartitionState{
							{
								Topic:     "the-topic",
								Partition: 1,
								ISR:       []int32{1},
								Replicas:  []int32{1},
								Leader:    1,
								ZKVersion: 1,
							},
						},
					}},
				},
				responses: []jocko.Response{{
					Header: &protocol.RequestHeader{CorrelationID: 2},
					Response: &protocol.Response{CorrelationID: 2, Body: &protocol.LeaderAndISRResponse{
						Partitions: []*protocol.LeaderAndISRPartition{
							{
								ErrorCode: protocol.ErrNone.Code(),
								Partition: 1,
								Topic:     "the-topic",
							},
						},
					}}}},
			},
		},
		{
			name:   "leader and isr leader become follower",
			fields: newFields(),
			setFields: func(f *fields) {
				f.topicMap = map[string][]*jocko.Partition{
					"the-topic": []*jocko.Partition{{
						Topic:                   "the-topic",
						ID:                      1,
						Replicas:                nil,
						ISR:                     nil,
						Leader:                  1,
						PreferredLeader:         1,
						LeaderAndISRVersionInZK: 0,
					}},
				}
			},
			args: args{
				requestCh:  make(chan jocko.Request, 2),
				responseCh: make(chan jocko.Response, 2),
				requests: []jocko.Request{{
					Header: &protocol.RequestHeader{CorrelationID: 2},
					Request: &protocol.LeaderAndISRRequest{
						PartitionStates: []*protocol.PartitionState{
							{
								Topic:     "the-topic",
								Partition: 1,
								ISR:       []int32{1},
								Replicas:  []int32{1},
								Leader:    0,
								ZKVersion: 1,
							},
						},
					}},
				},
				responses: []jocko.Response{{
					Header: &protocol.RequestHeader{CorrelationID: 2},
					Response: &protocol.Response{CorrelationID: 2, Body: &protocol.LeaderAndISRResponse{
						Partitions: []*protocol.LeaderAndISRPartition{
							{
								ErrorCode: protocol.ErrNone.Code(),
								Partition: 1,
								Topic:     "the-topic",
							},
						},
					}}}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setFields != nil {
				tt.setFields(&tt.fields)
			}
			b, err := New(config, tt.fields.logger)
			require.NoError(t, err)
			require.NotNil(t, b)
			defer func() {
				b.purge()
				b.Leave()
				b.Shutdown()
			}()
			retry.Run(t, func(r *retry.R) {
				if len(b.serverLookup.Servers()) != 1 {
					r.Fatal("server not added")
				}
			})
			if tt.fields.topicMap != nil {
				b.topicMap = tt.fields.topicMap
				for _, ps := range tt.fields.topicMap {
					for _, p := range ps {
						b.startReplica(p)
					}
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			go b.Run(ctx, tt.args.requestCh, tt.args.responseCh)

			for i := 0; i < len(tt.args.requests); i++ {
				tt.args.requestCh <- tt.args.requests[i]
				response := <-tt.args.responseCh

				switch res := response.Response.(*protocol.Response).Body.(type) {
				// handle timestamp explicitly since we don't know what
				// it'll be set to
				case *protocol.ProduceResponses:
					for _, response := range res.Responses {
						for _, pr := range response.PartitionResponses {
							if pr.ErrorCode != protocol.ErrNone.Code() {
								break
							}
							if pr.Timestamp == 0 {
								t.Error("expected timestamp not to be 0")
							}
							pr.Timestamp = 0
						}
					}
				}

				if !reflect.DeepEqual(response.Response, tt.args.responses[i].Response) {
					t.Errorf("got %s, want: %s", spewstr(response.Response), spewstr(tt.args.responses[i].Response))
				}

			}
			cancel()
		})
	}
}

func spewstr(v interface{}) string {
	var buf bytes.Buffer
	spew.Fdump(&buf, v)
	return buf.String()
}

// func TestBroker_Join(t *testing.T) {
// 	type args struct {
// 		addrs []string
// 	}
// 	err := errors.New("mock serf join error")
// 	tests := []struct {
// 		name      string
// 		fields    fields
// 		setFields func(f *fields)
// 		args      args
// 		want      protocol.Error
// 	}{
// 		{
// 			name:   "ok",
// 			fields: newFields(),
// 			args:   args{addrs: []string{"localhost:9082"}},
// 			want:   protocol.ErrNone,
// 		},
// 		{
// 			name:   "serf errr",
// 			fields: newFields(),
// 			setFields: func(f *fields) {
// 				f.serf.JoinFunc = func(addrs ...string) (int, error) {
// 					return -1, err
// 				}
// 			},
// 			args: args{addrs: []string{"localhost:9082"}},
// 			want: protocol.ErrUnknown.WithErr(err),
// 		},
// 	}
// 	for _, tt := range tests {
// 		t.Run(tt.name, func(t *testing.T) {
// 			if tt.setFields != nil {
// 				tt.setFields(&tt.fields)
// 			}
// 			b, err := New(&Config{
// 				ID:      tt.fields.id,
// 				DataDir: tt.fields.logDir,
// 				Addr:    tt.fields.brokerAddr,
// 			},  tt.fields.logger)
// 			if err != nil {
// 				t.Error("expected no err")
// 			}
// 			if got := b.JoinLAN(tt.args.addrs...); !reflect.DeepEqual(got, tt.want) {
// 				t.Errorf("Join() = %v, want %v", got, tt.want)
// 			}
// 			if !tt.fields.serf.JoinCalled() {
// 				t.Error("expected serf join invoked; did not")
// 			}
// 		})
// 	}
// }

func TestBroker_topicPartitions(t *testing.T) {
	type fields struct {
		logger      log.Logger
		id          int32
		topicMap    map[string][]*jocko.Partition
		replicators map[*jocko.Partition]*Replicator
		brokerAddr  string
		logDir      string
		raft        jocko.Raft
		serf        jocko.Serf
		shutdownCh  chan struct{}
		shutdown    bool
	}
	type args struct {
		topic string
	}
	tests := []struct {
		name      string
		fields    fields
		args      args
		wantFound []*jocko.Partition
		wantErr   protocol.Error
	}{
		{
			name: "partitions found",
			fields: fields{
				topicMap: map[string][]*jocko.Partition{"topic": []*jocko.Partition{{ID: 1}}},
				logger:   log.New(),
			},
			args:      args{topic: "topic"},
			wantFound: []*jocko.Partition{{ID: 1}},
			wantErr:   protocol.ErrNone,
		},
		{
			name: "partitions not found",
			fields: fields{
				topicMap: map[string][]*jocko.Partition{"topic": []*jocko.Partition{{ID: 1}}},
				logger:   log.New(),
			},
			args:      args{topic: "not_topic"},
			wantFound: nil,
			wantErr:   protocol.ErrUnknownTopicOrPartition,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, config := testutil.TestConfig(t)
			os.RemoveAll(dir)
			b, err := New(config, tt.fields.logger)
			if err != nil {
				t.Error("expected no err")
			}
			b.topicMap = tt.fields.topicMap
			gotFound, gotErr := b.topicPartitions(tt.args.topic)
			if !reflect.DeepEqual(gotFound, tt.wantFound) {
				t.Errorf("topicPartitions() gotFound = %v, want %v", gotFound, tt.wantFound)
			}
			if !reflect.DeepEqual(gotErr, tt.wantErr) {
				t.Errorf("topicPartitions() gotErr = %v, want %v", gotErr, tt.wantErr)
			}
		})
	}
}

func TestBroker_topics(t *testing.T) {
	type fields struct {
		logger      log.Logger
		id          int32
		topicMap    map[string][]*jocko.Partition
		replicators map[*jocko.Partition]*Replicator
		brokerAddr  string
		logDir      string
		raft        jocko.Raft
		serf        jocko.Serf
		shutdownCh  chan struct{}
		shutdown    bool
	}
	topicMap := map[string][]*jocko.Partition{
		"topic": []*jocko.Partition{{ID: 1}},
	}
	tests := []struct {
		name   string
		fields fields
		want   map[string][]*jocko.Partition
	}{
		{
			name: "topic map returned",
			fields: fields{
				topicMap: topicMap,
				logger:   log.New(),
			},
			want: topicMap,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, config := testutil.TestConfig(t)
			os.RemoveAll(dir)
			b, err := New(config, tt.fields.logger)
			if err != nil {
				t.Error("expected no err")
			}
			if tt.fields.topicMap != nil {
				b.topicMap = tt.fields.topicMap
			}
			if got := b.topics(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("topics() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBroker_partition(t *testing.T) {
	f := newFields()
	f.topicMap = map[string][]*jocko.Partition{
		"the-topic":   []*jocko.Partition{{ID: 1}},
		"empty-topic": []*jocko.Partition{},
	}
	type args struct {
		topic     string
		partition int32
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *jocko.Partition
		wanterr protocol.Error
	}{
		{
			name:   "found partitions",
			fields: f,
			args: args{
				topic:     "the-topic",
				partition: 1,
			},
			want:    f.topicMap["the-topic"][0],
			wanterr: protocol.ErrNone,
		},
		{
			name:   "no partitions",
			fields: f,
			args: args{
				topic:     "not-the-topic",
				partition: 1,
			},
			want:    nil,
			wanterr: protocol.ErrUnknownTopicOrPartition,
		},
		{
			name:   "empty partitions",
			fields: f,
			args: args{
				topic:     "empty-topic",
				partition: 1,
			},
			want:    nil,
			wanterr: protocol.ErrUnknownTopicOrPartition,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, config := testutil.TestConfig(t)
			os.RemoveAll(dir)
			b, err := New(config, tt.fields.logger)
			if err != nil {
				t.Error("expected no err")
			}
			if tt.fields.topicMap != nil {
				b.topicMap = tt.fields.topicMap
			}
			got, goterr := b.partition(tt.args.topic, tt.args.partition)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("partition() got = %v, want %v", got, tt.want)
			}
			if !reflect.DeepEqual(goterr, tt.wanterr) {
				t.Errorf("partition() goterr = %v, want %v", goterr, tt.wanterr)
			}
		})
	}
}

// func TestBroker_createPartition(t *testing.T) {
// 	type fields struct {
// 		logger      log.Logger
// 		id          int32
// 		topicMap    map[string][]*jocko.Partition
// 		replicators map[*jocko.Partition]*Replicator
// 		brokerAddr  string
// 		logDir      string
// 		raft        jocko.Raft
// 		serf        jocko.Serf
// 		shutdownCh  chan struct{}
// 		shutdown    bool
// 	}
// 	type args struct {
// 		partition *jocko.Partition
// 	}
// 	raft := &mock.Raft{}
// 	tests := []struct {
// 		name    string
// 		fields  fields
// 		args    args
// 		wantErr bool
// 	}{
// 		{
// 			name: "called apply",
// 			fields: fields{
// 				raft:   raft,
// 				logger: log.New(),
// 			},
// 			args:    args{partition: &jocko.Partition{ID: 1}},
// 			wantErr: false,
// 		},
// 	}
// 	for _, tt := range tests {
// 		t.Run(tt.name, func(t *testing.T) {
// 			dir, config := testutil.TestConfig(t)
// 			os.RemoveAll(dir)
// 			b, err := New(config,  tt.fields.logger)
// 			if err != nil {
// 				t.Error("expected no err")
// 			}
// 			if err := b.createPartition(tt.args.partition); (err != nil) != tt.wantErr {
// 				t.Errorf("createPartition() error = %v, wantErr %v", err, tt.wantErr)
// 			}
// 			if !raft.ApplyCalled() {
// 				t.Errorf("createPartition() raft.ApplyCalled() = %v, want %v", raft.ApplyCalled(), true)
// 			}
// 		})
// 	}
// }

func TestBroker_startReplica(t *testing.T) {
	type args struct {
		partition *jocko.Partition
	}
	partition := &jocko.Partition{
		Topic:  "the-topic",
		ID:     1,
		Leader: 1,
	}
	tests := []struct {
		name      string
		setFields func(f *fields)
		args      args
		want      protocol.Error
	}{
		{
			name: "started replica as leader",
			args: args{
				partition: partition,
			},
			want: protocol.ErrNone,
		},
		{
			name: "started replica as follower",
			args: args{
				partition: &jocko.Partition{
					ID:       1,
					Topic:    "replica-topic",
					Replicas: []int32{1},
					Leader:   2,
				},
			},
			want: protocol.ErrNone,
		},
		{
			name: "started replica with existing topic",
			setFields: func(f *fields) {
				f.topicMap["existing-topic"] = []*jocko.Partition{
					{
						ID:    1,
						Topic: "existing-topic",
					},
				}
			},
			args: args{
				partition: &jocko.Partition{ID: 2, Topic: "existing-topic"},
			},
			want: protocol.ErrNone,
		},
		// TODO: Possible bug. If a duplicate partition is added,
		//   the partition will be appended to the partitions as a duplicate.
		// {
		// 	name:   "started replica with dupe partition",
		// 	fields: f,
		// 	args: args{
		// 		partition: &jocko.Partition{ID: 1, Topic: "existing-topic"},
		// 	},
		// 	want: protocol.ErrNone,
		// },
		// {
		// 	name: "started replica with commitlog error",
		// 	setFields: func(f *fields) {
		// 		f.logDir = ""
		// 	},
		// 	args: args{
		// 		partition: &jocko.Partition{Leader: 1},
		// 	},
		// 	want: protocol.ErrUnknown.WithErr(errors.New("mkdir failed: mkdir /0: permission denied")),
		// },
	}
	for _, tt := range tests {
		fields := newFields()
		if tt.setFields != nil {
			tt.setFields(&fields)
		}
		t.Run(tt.name, func(t *testing.T) {
			dir, config := testutil.TestConfig(t)
			os.RemoveAll(dir)
			b, err := New(config, fields.logger)
			if err != nil {
				t.Error("expected no err")
			}
			if got := b.startReplica(tt.args.partition); got.Error() != tt.want.Error() {
				t.Errorf("startReplica() = %v, want %v", got, tt.want)
			}
			got, err := b.partition(tt.args.partition.Topic, tt.args.partition.ID)
			if !reflect.DeepEqual(got, tt.args.partition) {
				t.Errorf("partition() = %v, want %v", got, partition)
			}
			parts := map[int32]*jocko.Partition{}
			for _, p := range b.topicMap[tt.args.partition.Topic] {
				if _, ok := parts[p.ID]; ok {
					t.Errorf("topicPartition contains dupes, dupe %v", p)
				}
				parts[p.ID] = p
			}
			if err != protocol.ErrNone {
				t.Errorf("partition() err = %v, want %v", err, protocol.ErrNone)
			}
		})
	}
}

func TestBroker_createTopic(t *testing.T) {
	type fields struct {
		logger      log.Logger
		id          int32
		topicMap    map[string][]*jocko.Partition
		replicators map[*jocko.Partition]*Replicator
		brokerAddr  string
		logDir      string
		raft        jocko.Raft
		serf        jocko.Serf
		shutdownCh  chan struct{}
		shutdown    bool
	}
	type args struct {
		topic             string
		partitions        int32
		replicationFactor int16
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   protocol.Error
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, config := testutil.TestConfig(t)
			os.RemoveAll(dir)
			b, err := New(config, tt.fields.logger)
			if err != nil {
				t.Error("expected no err")
			}
			if got := b.createTopic(tt.args.topic, tt.args.partitions, tt.args.replicationFactor); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("createTopic() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBroker_deleteTopic(t *testing.T) {
	type fields struct {
		logger      log.Logger
		id          int32
		topicMap    map[string][]*jocko.Partition
		replicators map[*jocko.Partition]*Replicator
		brokerAddr  string
		logDir      string
		raft        jocko.Raft
		serf        jocko.Serf
		shutdownCh  chan struct{}
		shutdown    bool
	}
	type args struct {
		topic string
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   protocol.Error
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, config := testutil.TestConfig(t)
			os.RemoveAll(dir)
			b, err := New(config, tt.fields.logger)
			if err != nil {
				t.Error("expected no err")
			}
			if got := b.deleteTopic(tt.args.topic); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("deleteTopic() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBroker_deletePartitions(t *testing.T) {
	type fields struct {
		logger      log.Logger
		id          int32
		topicMap    map[string][]*jocko.Partition
		replicators map[*jocko.Partition]*Replicator
		brokerAddr  string
		logDir      string
		raft        jocko.Raft
		serf        jocko.Serf
		shutdownCh  chan struct{}
		shutdown    bool
	}
	type args struct {
		tp *jocko.Partition
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr bool
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, config := testutil.TestConfig(t)
			os.RemoveAll(dir)
			b, err := New(config, tt.fields.logger)
			if err != nil {
				t.Error("expected no err")
			}
			if err := b.deletePartitions(tt.args.tp); (err != nil) != tt.wantErr {
				t.Errorf("deletePartitions() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestBroker_Shutdown(t *testing.T) {
	tests := []struct {
		name    string
		fields  fields
		wantErr bool
	}{
		{
			name:    "shutdown ok",
			fields:  newFields(),
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, config := testutil.TestConfig(t)
			os.RemoveAll(dir)
			b, err := New(config, tt.fields.logger)
			if err != nil {
				t.Error("expected no err")
			}
			if err != nil {
				t.Errorf("New() error = %v, wanted nil", err)
			}
			if err := b.Shutdown(); (err != nil) != tt.wantErr {
				t.Errorf("Shutdown() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestBroker_becomeFollower(t *testing.T) {
	type fields struct {
		logger      log.Logger
		id          int32
		topicMap    map[string][]*jocko.Partition
		replicators map[*jocko.Partition]*Replicator
		brokerAddr  string
		logDir      string
		raft        jocko.Raft
		serf        jocko.Serf
		shutdownCh  chan struct{}
		shutdown    bool
	}
	type args struct {
		topic          string
		partitionID    int32
		partitionState *protocol.PartitionState
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   protocol.Error
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, config := testutil.TestConfig(t)
			os.RemoveAll(dir)
			b, err := New(config, tt.fields.logger)
			if err != nil {
				t.Error("expected no err")
			}
			if got := b.becomeFollower(tt.args.topic, tt.args.partitionID, tt.args.partitionState); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("becomeFollower() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBroker_becomeLeader(t *testing.T) {
	type fields struct {
		logger      log.Logger
		id          int32
		topicMap    map[string][]*jocko.Partition
		replicators map[*jocko.Partition]*Replicator
		brokerAddr  string
		logDir      string
		raft        jocko.Raft
		serf        jocko.Serf
		shutdownCh  chan struct{}
		shutdown    bool
	}
	type args struct {
		topic          string
		partitionID    int32
		partitionState *protocol.PartitionState
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   protocol.Error
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, config := testutil.TestConfig(t)
			os.RemoveAll(dir)
			b, err := New(config, tt.fields.logger)
			if err != nil {
				t.Error("expected no err")
			}
			if got := b.becomeLeader(tt.args.topic, tt.args.partitionID, tt.args.partitionState); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("becomeLeader() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_contains(t *testing.T) {
	type args struct {
		rs []int32
		r  int32
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := contains(tt.args.rs, tt.args.r); got != tt.want {
				t.Errorf("contains() = %v, want %v", got, tt.want)
			}
		})
	}
}

type fields struct {
	id           int32
	serf         *mock.Serf
	raft         *mock.Raft
	raftCommands chan jocko.RaftCommand
	logger       log.Logger
	topicMap     map[string][]*jocko.Partition
	replicators  map[*jocko.Partition]*Replicator
	brokerAddr   string
	loner        bool
	logDir       string
	shutdownCh   chan struct{}
	shutdown     bool
}

func newFields() fields {
	return fields{
		topicMap:     make(map[string][]*jocko.Partition),
		raftCommands: make(chan jocko.RaftCommand),
		replicators:  make(map[*jocko.Partition]*Replicator),
		logger:       log.New(),
		logDir:       "/tmp/jocko/logs",
		loner:        true,
		brokerAddr:   "localhost:9092",
		id:           1,
	}
}

func TestBroker_JoinLAN(t *testing.T) {
	logger := log.New()
	dir1, config1 := testutil.TestConfig(t)
	b1, err := New(config1, logger)
	require.NoError(t, err)
	os.RemoveAll(dir1)

	dir2, config2 := testutil.TestConfig(t)
	b2, err := New(config2, logger)
	os.RemoveAll(dir2)
	require.NoError(t, err)
	joinLAN(t, b1, b2)

	retry.Run(t, func(r *retry.R) {
		require.Equal(t, 2, len(b1.LANMembers()))
		require.Equal(t, 2, len(b2.LANMembers()))
	})
}

func TestBroker_RegisterMember(t *testing.T) {
	logger := log.New()
	dir1, config1 := testutil.TestConfig(t)
	config1.Bootstrap = true
	config1.BootstrapExpect = 3
	b1, err := New(config1, logger)
	require.NoError(t, err)
	os.RemoveAll(dir1)

	dir2, config2 := testutil.TestConfig(t)
	config2.Bootstrap = false
	config2.BootstrapExpect = 3
	b2, err := New(config2, logger)
	os.RemoveAll(dir2)
	require.NoError(t, err)

	joinLAN(t, b2, b1)

	waitForLeader(t, b1, b2)

	state := b1.fsm.State()
	retry.Run(t, func(r *retry.R) {
		_, node, err := state.GetNode(b2.config.RaftAddr)
		if err != nil {
			r.Fatalf("err: %v", err)
		}
		if node == nil {
			r.Fatal("node not registered")
		}
	})
	retry.Run(t, func(r *retry.R) {
		_, node, err := state.GetNode(b1.config.RaftAddr)
		if err != nil {
			r.Fatalf("err: %v", err)
		}
		if node == nil {
			r.Fatal("node not registered")
		}
	})
}

func TestBroker_FailedMember(t *testing.T) {
	logger := log.New()
	dir1, config1 := testutil.TestConfig(t)
	config1.Bootstrap = true
	config1.BootstrapExpect = 2
	b1, err := New(config1, logger)
	require.NoError(t, err)
	os.RemoveAll(dir1)

	dir2, config2 := testutil.TestConfig(t)
	config2.Bootstrap = false
	config2.BootstrapExpect = 2
	config2.NonVoter = true
	b2, err := New(config2, logger)
	os.RemoveAll(dir2)
	require.NoError(t, err)

	waitForLeader(t, b1, b2)

	joinLAN(t, b2, b1)

	// Fail the member
	b2.Shutdown()

	// Should be registered
	state := b1.fsm.State()
	retry.Run(t, func(r *retry.R) {
		_, node, err := state.GetNode(b2.config.RaftAddr)
		if err != nil {
			r.Fatalf("err: %v", err)
		}
		if node == nil {
			r.Fatal("node not registered")
		}
	})

	// todo: check have failed checks
}

func TestBroker_LeftMember(t *testing.T) {
	logger := log.New()
	dir1, config1 := testutil.TestConfig(t)
	config1.Bootstrap = true
	config1.BootstrapExpect = 2
	b1, err := New(config1, logger)
	require.NoError(t, err)
	os.RemoveAll(dir1)

	dir2, config2 := testutil.TestConfig(t)
	config2.Bootstrap = false
	config2.BootstrapExpect = 2
	config2.NonVoter = true
	b2, err := New(config2, logger)
	os.RemoveAll(dir2)
	require.NoError(t, err)

	waitForLeader(t, b1, b2)

	joinLAN(t, b2, b1)

	// Fail the member
	b2.Leave()
	b2.Shutdown()

	// Should be deregistered
	state := b1.fsm.State()
	retry.Run(t, func(r *retry.R) {
		_, node, err := state.GetNode(b2.config.RaftAddr)
		if err != nil {
			r.Fatalf("err: %v", err)
		}
		if node != nil {
			r.Fatal("node still registered")
		}
	})
}

func TestBroker_LeaveLeader(t *testing.T) {
	logger := log.New()
	dir1, config1 := testutil.TestConfig(t)
	config1.Bootstrap = true
	config1.BootstrapExpect = 3
	b1, err := New(config1, logger)
	require.NoError(t, err)
	defer os.RemoveAll(dir1)

	dir2, config2 := testutil.TestConfig(t)
	config2.Bootstrap = false
	config2.BootstrapExpect = 3
	b2, err := New(config2, logger)
	defer os.RemoveAll(dir2)
	require.NoError(t, err)

	dir3, config3 := testutil.TestConfig(t)
	config3.Bootstrap = false
	config3.BootstrapExpect = 3
	b3, err := New(config3, logger)
	defer os.RemoveAll(dir3)
	require.NoError(t, err)

	brokers := []*Broker{b1, b2, b3}

	joinLAN(t, b2, b1)
	joinLAN(t, b3, b1)

	for _, b := range brokers {
		retry.Run(t, func(r *retry.R) {
			r.Check(wantPeers(b, 3))
		})
	}

	var leader *Broker
	for _, b := range brokers {
		if b.isLeader() {
			leader = b
			break
		}
	}

	if leader == nil {
		t.Fatal("no leader")
	}

	if !leader.isReadyForConsistentReads() {
		t.Fatal("leader should be ready for consistent reads")
	}

	err = leader.Leave()
	require.NoError(t, err)

	if leader.isReadyForConsistentReads() {
		t.Fatal("leader should not be ready for consistent reads")
	}

	leader.Shutdown()

	var remain *Broker
	for _, b := range brokers {
		if b == leader {
			continue
		}
		remain = b
		retry.Run(t, func(r *retry.R) { r.Check(wantPeers(b, 2)) })
	}

	retry.Run(t, func(r *retry.R) {
		for _, b := range brokers {
			if leader == b && b.isLeader() {
				r.Fatal("should have new leader")
			}
		}
	})

	state := remain.fsm.State()
	retry.Run(t, func(r *retry.R) {
		_, node, err := state.GetNode(leader.config.RaftAddr)
		if err != nil {
			r.Fatalf("err: %v", err)
		}
		if node != nil {
			r.Fatal("leader should be deregistered")
		}
	})
}

func waitForLeader(t *testing.T, brokers ...*Broker) {
	retry.Run(t, func(r *retry.R) {
		for _, b := range brokers {
			if raft.Leader == b.raft.State() {
				t.Fatal("no leader")
			}
		}
	})
}

func joinLAN(t *testing.T, b1 *Broker, b2 *Broker) {
	addr := fmt.Sprintf("127.0.0.1:%d", b2.config.SerfLANConfig.MemberlistConfig.BindPort)
	err := b1.JoinLAN(addr)
	require.Equal(t, err, protocol.ErrNone)
}

type nopReaderWriter struct{}

func (nopReaderWriter) Read(b []byte) (int, error)  { return 0, nil }
func (nopReaderWriter) Write(b []byte) (int, error) { return 0, nil }
func newNopReaderWriter() io.ReadWriter             { return nopReaderWriter{} }

// purge is a helper function to delete all topics and partitions for this  likely only useful for tests.
func (s *Broker) purge() error {
	s.Lock()
	defer s.Unlock()
	for topic, partitions := range s.topicMap {
		for _, p := range partitions {
			if err := p.Delete(); err != nil {
				return err
			}
		}
		delete(s.topicMap, topic)
	}
	return nil
}

// wantPeers determines whether the server has the given
// number of voting raft peers.
func wantPeers(s *Broker, peers int) error {
	n, err := s.numPeers()
	if err != nil {
		return err
	}
	if got, want := n, peers; got != want {
		return fmt.Errorf("got %d peers want %d", got, want)
	}
	return nil
}
