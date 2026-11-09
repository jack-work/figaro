package angelus

import (
	"encoding/json"
	"fmt"

	"github.com/jack-work/figaro/api/rpc"
)

type channels uint8

const (
	transcriptChannel channels = 1 << iota
	formsChannel

	allChannels = transcriptChannel | formsChannel
)

func channelOf(method string) channels {
	switch method {
	case rpc.MethodAriaFrame, rpc.MethodTurnDone:
		return transcriptChannel
	case rpc.MethodFormDelta:
		return formsChannel
	}
	return allChannels
}

func followChannels(params json.RawMessage) (channels, error) {
	var req rpc.FollowRequest
	if len(params) > 0 {
		if err := json.Unmarshal(params, &req); err != nil {
			return 0, fmt.Errorf("%s: %w", rpc.MethodFollow, err)
		}
	}
	if len(req.Channels) == 0 {
		return allChannels, nil
	}
	var ch channels
	for _, name := range req.Channels {
		switch name {
		case rpc.FollowTranscript:
			ch |= transcriptChannel
		case rpc.FollowForms:
			ch |= formsChannel
		default:
			return 0, fmt.Errorf("%s: no channel %q; a follow names %q, %q or both", rpc.MethodFollow, name, rpc.FollowTranscript, rpc.FollowForms)
		}
	}
	return ch, nil
}
