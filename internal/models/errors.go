package models

import "fmt"

type NotFoundError struct {
	Model string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("unsupported model %q", e.Model)
}
