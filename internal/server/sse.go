package server

import "io"

func writeSSE(w io.Writer, eventName string, payload []byte) {
	if eventName != "" {
		_, _ = io.WriteString(w, "event: "+eventName+"\n")
	}
	_, _ = io.WriteString(w, "data: ")
	_, _ = w.Write(payload)
	_, _ = io.WriteString(w, "\n\n")
}
