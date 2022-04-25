package om

import (
	"strconv"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
)

// ReplicaSetWithProcesses is a wrapper for replica set and processes that match to it
// The contract for class is that both processes and replica set are guaranteed to match to each other
// Note, that the type modifies the entities directly and doesn't create copies! (seems not a big deal for clients)
type ReplicaSetWithProcesses struct {
	Rs        ReplicaSet
	Processes []Process
}

// NewReplicaSetWithProcesses is the only correct function for creation ReplicaSetWithProcesses
func NewReplicaSetWithProcesses(
	rs ReplicaSet,
	processes []Process,
) ReplicaSetWithProcesses {
	rs.clearMembers()

	for _, p := range processes {
		p.setReplicaSetName(rs.Name())
		rs.addMember(p, "")
	}
	return ReplicaSetWithProcesses{rs, processes}
}

// determineNextProcessIdStartingPoint returns the number which should be used as a starting
// point for generating new _ids.
func determineNextProcessIdStartingPoint(desiredProcesses []Process, existingProcessIds map[string]int) int {
	// determine the next id, it has to be higher than any previous value
	newId := 0
	for _, id := range existingProcessIds {
		if id >= newId {
			newId = id + 1
		}
	}
	return newId
}

// NewMultiClusterReplicaSetWithProcesses Creates processes for a multi cluster deployment.
// This function ensures that new proccesses which are added never have an overlapping _id with any existing process.
// existing _ids are re-used, and when new processes are added, a new higher number is used.
func NewMultiClusterReplicaSetWithProcesses(rs ReplicaSet, processes []Process, existingProcessIds map[string]int, connectivity *mdbv1.MongoDBConnectivity) ReplicaSetWithProcesses {
	newId := determineNextProcessIdStartingPoint(processes, existingProcessIds)
	rs.clearMembers()
	for _, p := range processes {
		p.setReplicaSetName(rs.Name())
		// ensure the process id is not changed if it already exists
		if existingId, ok := existingProcessIds[p.Name()]; ok {
			rs.addMember(p, strconv.Itoa(existingId))
		} else {
			// otherwise add a new id which is always incrementing
			rs.addMember(p, strconv.Itoa(newId))
			newId++
		}
	}
	fullRs := ReplicaSetWithProcesses{Rs: rs, Processes: processes}
	if connectivity != nil {
		fullRs.SetHorizons(connectivity.ReplicaSetHorizons)
	}
	return fullRs
}

func (r ReplicaSetWithProcesses) GetProcessNames() []string {
	processNames := make([]string, len(r.Processes))
	for i, p := range r.Processes {
		processNames[i] = p.Name()
	}
	return processNames
}

func (r ReplicaSetWithProcesses) SetHorizons(replicaSetHorizons []mdbv1.MongoDBHorizonConfig) {
	if len(replicaSetHorizons) >= len(r.Rs.Members()) {
		for i, m := range r.Rs.Members() {
			m.setHorizonConfig(replicaSetHorizons[i])
		}
	}
}
