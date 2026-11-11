package web

import (
	"context"
	"errors"
	"strings"

	"github.com/scott/castbot/internal/host"
)

// testMachineName is the throwaway prefix the live harness may delete.
// castbot-s07c-test- is included so a later live run can reuse this sweep.
func testMachineName(name string) bool {
	return strings.HasPrefix(name, "castbot-s07-test-") || strings.HasPrefix(name, "castbot-s07c-test-")
}

// machineSweeper is the list, status, and delete surface the live cleanup uses.
type machineSweeper interface {
	List(context.Context) ([]host.Machine, error)
	Status(context.Context, string) (host.Machine, error)
	Delete(context.Context, string) error
}

// sweepThrowawayMachines deletes every castbot-s07-test-* and
// castbot-s07c-test-* machine. A name outside those prefixes is left
// alone, and GuardDelete still refuses a demo or probe machine.
func sweepThrowawayMachines(ctx context.Context, api machineSweeper) error {
	listed, err := api.List(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, vm := range listed {
		if vm.ID == "" || !testMachineName(vm.Name) {
			continue
		}
		if err := deleteThrowaway(ctx, api, vm.ID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func deleteThrowaway(ctx context.Context, api machineSweeper, id string) error {
	if id == "" {
		return nil
	}
	got, err := api.Status(ctx, id)
	if err != nil {
		if strings.Contains(err.Error(), "404") {
			return nil
		}
		return err
	}
	if !testMachineName(got.Name) {
		return errors.New("refusing to delete a machine outside the test prefix")
	}
	if err := host.GuardDelete(got); err != nil {
		return err
	}
	return api.Delete(ctx, id)
}
