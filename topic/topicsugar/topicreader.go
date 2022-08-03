package topicsugar

import (
	"encoding/json"

	"google.golang.org/protobuf/proto"

	"github.com/ydb-platform/ydb-go-sdk/v3/topic/topicreader"
)

// ProtoUnmarshal unmarshal message content to protobuf struct
//
// Experimental
//
// Notice: This API is EXPERIMENTAL and may be changed or removed in a later release.
func ProtoUnmarshal(msg *topicreader.Message, dst proto.Message) error {
	return msg.UnmarshalTo(protobufUnmarshaler{dst: dst})
}

// JSONUnmarshal unmarshal json message content to dst
// dst must by pointer to struct
//
// Experimental
//
// Notice: This API is EXPERIMENTAL and may be changed or removed in a later release.
func JSONUnmarshal(msg *topicreader.Message, dst interface{}) error {
	return UnmarshalMessageWith(msg, json.Unmarshal, dst)
}

// UnmarshalMessageWith call unmarshaller func with message content
// unmarshaller func must not use received byte slice after return.
//
// Experimental
//
// Notice: This API is EXPERIMENTAL and may be changed or removed in a later release.
func UnmarshalMessageWith(msg *topicreader.Message, unmarshaler UnmarshalFunc, v interface{}) error {
	return msg.UnmarshalTo(messageUnmarshaler{unmarshaler: unmarshaler, dst: v})
}

// ReadMessageDataWithCallback receive full content of message as data
// data slice MUST not be used after return from f.
// if you need content after return from function - copy it with
// copy(dst, data) to another byte slice
//
// Experimental
//
// Notice: This API is EXPERIMENTAL and may be changed or removed in a later release.
func ReadMessageDataWithCallback(msg *topicreader.Message, f func(data []byte) error) error {
	return msg.UnmarshalTo(messageUnmarhalerToCallback(f))
}

type messageUnmarhalerToCallback func(data []byte) error

// UnmarshalYDBTopicMessage unmarshal implementation
//
// Experimental
//
// Notice: This API is EXPERIMENTAL and may be changed or removed in a later release.
func (c messageUnmarhalerToCallback) UnmarshalYDBTopicMessage(data []byte) error {
	return c(data)
}

// UnmarshalFunc is func to unmarshal data to interface, for example
// json.Unmarshal from standard library
//
// Experimental
//
// Notice: This API is EXPERIMENTAL and may be changed or removed in a later release.
type UnmarshalFunc func(data []byte, dst interface{}) error

type protobufUnmarshaler struct {
	dst proto.Message
}

// UnmarshalYDBTopicMessage implement unmarshaller
//
// Experimental
//
// Notice: This API is EXPERIMENTAL and may be changed or removed in a later release.
func (m protobufUnmarshaler) UnmarshalYDBTopicMessage(data []byte) error {
	return proto.Unmarshal(data, m.dst)
}

type messageUnmarshaler struct {
	unmarshaler UnmarshalFunc
	dst         interface{}
}

// UnmarshalYDBTopicMessage
//
// Experimental
//
// Notice: This API is EXPERIMENTAL and may be changed or removed in a later release.
func (m messageUnmarshaler) UnmarshalYDBTopicMessage(data []byte) error {
	return m.unmarshaler(data, m.dst)
}
