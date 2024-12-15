package jobs

import (
	"fmt"
)

func GetPathFromOutput(id string) string {
	return fmt.Sprintf("jobs/%s", id)
}
