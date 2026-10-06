package ids

import "github.com/google/uuid"

func New() string {
	id, err := uuid.NewV7()
	if err != nil {
		id = uuid.New()
	}
	return id.String()
}
