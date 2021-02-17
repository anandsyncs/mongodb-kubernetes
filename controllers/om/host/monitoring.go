package host

import (
	"errors"
	"fmt"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"

	"go.uber.org/zap"
)

// StopMonitoring will stop OM monitoring of hosts, which will then
// make OM stop displaying old hosts from Processes view.
// Note, that the method tries to delete as many hosts as possible and doesn't give up on errors, returns
// the last error instead
func StopMonitoring(getRemover GetRemover, hostnames []string, log *zap.SugaredLogger) error {
	if len(hostnames) == 0 {
		return nil
	}

	hosts, err := getRemover.GetHosts()
	if err != nil {
		return err
	}
	errorHappened := false
	for _, hostname := range hostnames {
		found := false
		for _, h := range hosts.Results {
			if h.Hostname == hostname {
				found = true
				err = getRemover.RemoveHost(h.Id)
				if err != nil {
					log.Warnf("Failed to remove host %s from monitoring in Ops Manager: %s", h.Hostname, err)
					errorHappened = true
				} else {
					log.Debugf("Removed the host %s from monitoring in Ops Manager", h.Hostname)
				}
				break
			}
		}
		if !found {
			log.Warnf("Unable to remove monitoring on host %s as it was not found", hostname)
		}
	}

	if errorHappened {
		return errors.New("Failed to remove some hosts from monitoring in Ops manager")
	}
	return nil
}

// stopMonitoringHosts removes monitoring for this list of hosts from Ops Manager.
func stopMonitoringHosts(getRemover GetRemover, hosts []string, log *zap.SugaredLogger) error {
	if len(hosts) == 0 {
		return nil
	}

	if err := StopMonitoring(getRemover, hosts, log); err != nil {
		return fmt.Errorf("Failed to stop monitoring on hosts %s: %s", hosts, err)
	}

	return nil
}

// CalculateDiffAndStopMonitoringHosts checks hosts that are present in hostsBefore but not hostsAfter, and removes
// monitoring from them.
func CalculateDiffAndStopMonitoring(getRemover GetRemover, hostsBefore, hostsAfter []string, log *zap.SugaredLogger) error {
	return stopMonitoringHosts(getRemover, util.FindLeftDifference(hostsBefore, hostsAfter), log)
}
