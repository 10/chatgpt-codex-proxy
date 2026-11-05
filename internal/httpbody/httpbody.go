package httpbody

import (
	"fmt"
	"io"
)

const Limit int64 = 32 * 1024

func ReadLimitedErrorBody(r io.Reader) string {
	body, err := io.ReadAll(io.LimitReader(r, Limit))
	if err != nil {
		return fmt.Sprintf("<failed to read upstream response body: %v>", err)
	}
	return string(body)
}
