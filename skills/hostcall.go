package main

import (
	"encoding/json"
	"errors"
	"fmt"
)

// callHostJSONProtocol owns the bounded guest-side half of the fixed host
// import protocol. Host imports return a negative JSON error length when the
// supplied buffer is too small, so this must allocate the declared ceiling on
// the first call; a positive required-size retry protocol does not exist.
func callHostJSONProtocol(request, response any, maxCapacity int32, call func([]byte, int32) (int32, []byte)) error {
	if maxCapacity <= 0 {
		return errors.New("host import capacity must be positive")
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return err
	}
	n, payload := call(raw, maxCapacity)
	length := int64(n)
	if length < 0 {
		length = -length
	}
	if length <= 0 {
		return errors.New("host import returned an empty response")
	}
	if length > int64(maxCapacity) || length > int64(len(payload)) {
		return fmt.Errorf("host import returned invalid length %d for %d-byte ceiling", n, maxCapacity)
	}
	payload = payload[:length]
	if n < 0 {
		var envelope struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(payload, &envelope) == nil && envelope.Error != "" {
			return errors.New(envelope.Error)
		}
		return fmt.Errorf("host import failed: %s", payload)
	}
	return json.Unmarshal(payload, response)
}
