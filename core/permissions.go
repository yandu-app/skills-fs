package core

func canAccess(caller CallerIdentity, uid, gid, mode uint32, op OpCode) bool {
	if op == OpStat || op == OpReaddir {
		return true
	}
	var bit uint32
	switch op {
	case OpRead:
		bit = 4
	case OpWrite:
		bit = 2
	default:
		return false
	}
	perm := mode & 0o7
	if caller.GID == gid {
		perm = (mode >> 3) & 0o7
	}
	if caller.UID == uid {
		perm = (mode >> 6) & 0o7
	}
	return perm&bit != 0
}
