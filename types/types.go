package types

import (
	"bytes"
	"time"
)

type JobCreateResponse struct {
	ID string `json:"id" description:"The id of the job"`
}

// @ci table=jobs
//
// Jobs are background processes that can be run on a coordinator server.
type Job struct {
	ID        string           `db:"id" json:"id" validate:"required" description:"The ID of the job."`
	Name      string           `db:"name" json:"name" validate:"required" description:"The name of the job."`
	Output    *Output          `db:"output" json:"output" description:"The output of the job."`
	Fields    map[string]any   `db:"fields" json:"fields" description:"The public fields of the job. Note that sensitive data may be omitted from storage entirely"`
	Statuses  []map[string]any `db:"statuses" json:"statuses" validate:"required" description:"The job statuses."`
	GuildID   string           `db:"guild_id" json:"guild_id" validate:"required" description:"The ID of the guild the job is for."`
	Expiry    *time.Duration   `db:"expiry" json:"expiry" validate:"required" description:"The job expiry."`
	State     string           `db:"state" json:"state" validate:"required" description:"The jobs' current state (pending/completed etc)."`
	Resumable bool             `db:"resumable" json:"resumable" description:"Whether the job is resumable."`
	CreatedAt time.Time        `db:"created_at" json:"created_at" description:"The time the job was created."`
}

// @ci table=jobs unfilled=1
//
// A PartialJob represents a partial representation of a job.
type PartialJob struct {
	ID        string         `db:"id" json:"id" validate:"required" description:"The ID of the job."`
	Name      string         `db:"name" json:"name" validate:"required" description:"The name of the job."`
	Expiry    *time.Duration `db:"expiry" json:"expiry" validate:"required" description:"The job expiry."`
	State     string         `db:"state" json:"state" validate:"required" description:"The jobs' current state (pending/completed etc)."`
	CreatedAt time.Time      `db:"created_at" json:"created_at" description:"The time the job was created."`
}

type JobListResponse struct {
	Jobs []PartialJob `json:"jobs" description:"The list of (partial) jobs"`
}

// Output is the output of a job
type Output struct {
	Filename string        `json:"filename"`
	Perguild bool          `json:"perguild"`
	Buffer   *bytes.Buffer `json:"-"`
}
