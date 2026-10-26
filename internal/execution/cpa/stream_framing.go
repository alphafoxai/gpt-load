package cpa

import (
	"bytes"
	"fmt"
)

const maxSubscriptionSSEEventBytes = 10 << 20

// nativeResponsesSSEAssembler 恢复 SDK 行块或原始传输字节中的完整 SSE 事件。
// rawSSE 为 true 时只信任上游分隔符，绝不为 HTTP 读取边界补换行。
type nativeResponsesSSEAssembler struct {
	pending    []byte
	rawSSE     bool
	scanOffset int
	lineStart  int
}

func (assembler *nativeResponsesSSEAssembler) push(chunk []byte) ([][]byte, error) {
	assembler.pending = append(assembler.pending, chunk...)
	if !assembler.rawSSE && (len(chunk) == 0 || chunk[len(chunk)-1] != '\n' && chunk[len(chunk)-1] != '\r') {
		assembler.pending = append(assembler.pending, '\n')
	}
	return assembler.drain(false)
}

func (assembler *nativeResponsesSSEAssembler) drain(final bool) ([][]byte, error) {
	var events [][]byte
	for {
		eventEnd, found := assembler.nextEventEnd(final)
		if !found {
			if len(assembler.pending) > maxSubscriptionSSEEventBytes {
				return nil, fmt.Errorf("subscription SSE event exceeds limit")
			}
			return events, nil
		}
		if eventEnd > maxSubscriptionSSEEventBytes {
			return nil, fmt.Errorf("subscription SSE event exceeds limit")
		}
		var event []byte
		if eventEnd == len(assembler.pending) {
			event, assembler.pending = assembler.pending, nil
		} else {
			event = bytes.Clone(assembler.pending[:eventEnd])
			assembler.pending = assembler.pending[eventEnd:]
		}
		assembler.scanOffset, assembler.lineStart = 0, 0
		if assembler.rawSSE || subscriptionSSEEventHasData(event) {
			events = append(events, event)
		}
	}
}

func (assembler *nativeResponsesSSEAssembler) finish() ([][]byte, error) {
	if assembler == nil {
		return nil, nil
	}
	if assembler.rawSSE {
		events, err := assembler.drain(true)
		if err != nil {
			return nil, err
		}
		if len(bytes.TrimSpace(assembler.pending)) != 0 {
			return nil, fmt.Errorf("subscription SSE stream ended with an incomplete event")
		}
		return events, nil
	}
	if len(bytes.TrimSpace(assembler.pending)) == 0 {
		return nil, nil
	}
	events, err := assembler.push(nil)
	if err != nil {
		return events, err
	}
	if len(events) == 0 || len(bytes.TrimSpace(assembler.pending)) != 0 {
		return events, fmt.Errorf("subscription SSE stream ended with an incomplete event")
	}
	return events, nil
}

func (assembler *nativeResponsesSSEAssembler) nextEventEnd(final bool) (int, bool) {
	data := assembler.pending
	for assembler.scanOffset < len(data) {
		index := assembler.scanOffset
		if data[index] != '\r' && data[index] != '\n' {
			assembler.scanOffset++
			continue
		}
		// CRLF 可以跨读取块，EOF 时最后一个 CR 则是完整的单字符行终止符。
		if data[index] == '\r' && index+1 == len(data) && !final {
			return 0, false
		}
		assembler.scanOffset++
		if data[index] == '\r' && assembler.scanOffset < len(data) && data[assembler.scanOffset] == '\n' {
			assembler.scanOffset++
		}
		if index == assembler.lineStart {
			return assembler.scanOffset, true
		}
		assembler.lineStart = assembler.scanOffset
	}
	return 0, false
}

func subscriptionSSEEventHasData(event []byte) bool {
	for offset := 0; offset < len(event); {
		lineEnd := bytes.IndexAny(event[offset:], "\r\n")
		line := event[offset:]
		if lineEnd >= 0 {
			lineEnd += offset
			line = event[offset:lineEnd]
		}
		if bytes.Equal(line, []byte("data")) || bytes.HasPrefix(line, []byte("data:")) {
			return true
		}
		if lineEnd < 0 {
			return false
		}
		offset = lineEnd + 1
		if event[lineEnd] == '\r' && offset < len(event) && event[offset] == '\n' {
			offset++
		}
	}
	return false
}
