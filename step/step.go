// In many cases such as restoring backups, jobs can be quite complex
// and should/can be broken down into smaller steps
//
// Step is an utility structure that allows breaking down jobs complete with persist support
package step

import (
	"fmt"
	"strconv"

	jobstate "github.com/Anti-Raid/jobserver/state"
	"github.com/Anti-Raid/jobserver/types"
	"go.uber.org/zap"
)

type Stepper[T any] struct {
	steps          []*Step[T]
	stepCache      map[string]*Step[T]
	stepIndexCache map[string]int
}

// NewStepper creates a new stepper
func NewStepper[T any](steps ...Step[T]) *Stepper[T] {
	var s = &Stepper[T]{
		steps:          []*Step[T]{},
		stepCache:      map[string]*Step[T]{},
		stepIndexCache: map[string]int{},
	}

	for i := range steps {
		step := steps[i]

		if step.State == "" {
			panic("step state cannot be empty")
		}

		// Ensure no duplicate steps
		if _, ok := s.stepCache[step.State]; ok {
			panic("duplicate step state")
		}

		if step.Index == 0 {
			step.Index = i
		}

		s.steps = append(s.steps, &step)
		s.stepCache[step.State] = &step
		s.stepIndexCache[step.State] = step.Index
	}

	return s
}

// Step returns a step based on its state
func (s *Stepper[T]) Step(state string) (*Step[T], bool) {
	if step, ok := s.stepCache[state]; ok {
		return step, true
	}

	return nil, false
}

// StepPosition returns the index of a step
func (s *Stepper[T]) StepIndex(state string) int {
	if pos, ok := s.stepIndexCache[state]; ok {
		return pos
	}

	return -1
}

// Exec executes all steps, skipping over steps with a lower index
func (s *Stepper[T]) Exec(
	self *T,
	l *zap.Logger,
	state jobstate.State,
	progstate jobstate.ProgressState,
) (*types.Output, error) {
	curProg, err := progstate.GetProgress()

	if err != nil {
		return nil, err
	}

	if curProg == nil {
		curProg = &jobstate.Progress{
			State: "",
			Data:  map[string]any{},
		}
	}

	for i := range s.steps {
		step := s.steps[i]

		select {
		case <-state.Context().Done():
			return nil, state.Context().Err()
		default:
			// Continue
		}

		// Conditions to run a step:
		//
		// 1. curProg.State is empty means the step will be executed
		// 2. curProg.State is not empty and is equal to the step state
		// 3. curProg.State is not empty and is not equal to the step state but the step index is greater than or equal to the current step index
		if curProg.State == "" || curProg.State == step.State || step.Index >= s.StepIndex(curProg.State) {
			l.Info("[" + strconv.Itoa(step.Index) + "] Executing step '" + step.State + "'")

			outp, prog, err := step.Exec(self, l, state, progstate, curProg)

			if err != nil {
				return nil, err
			}

			if outp != nil {
				return outp, nil
			}

			if prog != nil {
				if prog.State == "" {
					// Get the next step and use that for state
					if len(s.steps) > i {
						prog.State = s.steps[i+1].State
					} else {
						prog.State = "completed"
					}
				} else {
					// Ensure the next step is valid
					if _, ok := s.Step(prog.State); !ok {
						return nil, fmt.Errorf("invalid step state")
					}
				}

				curProg.State = prog.State // Update state

				if prog.Data != nil {
					// Prog is additive, add in all the elements from prog to curProg
					for k, v := range prog.Data {
						if v == nil {
							// Delete from curProg
							delete(curProg.Data, k)
						} else {
							curProg.Data[k] = v
						}
					}
				}

				err = progstate.SetProgress(curProg)

				if err != nil {
					return nil, err
				}
			}
		} else {
			l.Info("[" + strconv.Itoa(step.Index) + "] Skipping step '" + step.State + "' [resuming job?]")
		}
	}

	return nil, nil
}

type Step[T any] struct {
	State string

	// By default, steps of a lower index are ignored
	// Steps may however have an equal index in which case the step that is first in the array is first executed
	Index int

	// Exec will either return the output and/which ends the job, the new progress for the job
	// or an error to quickly abort the stepping
	Exec func(
		self *T,
		l *zap.Logger,
		state jobstate.State,
		progstate jobstate.ProgressState,
		progress *jobstate.Progress,
	) (*types.Output, *jobstate.Progress, error)
}
