package monitor

import (
	"math"
	"strings"
)

// AggregateDisks sums local capacity, counting each physical capacity once.
// Mounts backed by a network or FUSE source are excluded: their quota is not
// local block capacity, matching the original's systemDiskUsage rules.
func AggregateDisks(disks []Disk) (usedGB, totalGB, percent float64) {
	type group struct{ used, total float64 }
	groups := make(map[string]group, len(disks))
	for _, disk := range disks {
		if !Valid(disk.UsedGB) || !Valid(disk.TotalGB) ||
			disk.UsedGB < 0 || disk.TotalGB <= 0 {
			continue
		}
		if isNetworkOrFuseFilesystemType(disk.FilesystemType) ||
			(disk.FilesystemType == "" || disk.FilesystemType == "-") &&
				isNetworkOrFuseCapacityKey(disk.CapacityKey) {
			continue
		}
		identity := strings.TrimSpace(disk.CapacityKey)
		if identity == "" {
			identity = "mount:" + disk.MountPoint
		}
		existing := groups[identity]
		groups[identity] = group{
			used:  max(existing.used, disk.UsedGB),
			total: max(existing.total, disk.TotalGB),
		}
	}
	for _, value := range groups {
		usedGB += value.used
		totalGB += value.total
	}
	if totalGB <= 0 {
		return 0, 0, math.NaN()
	}
	percent = usedGB / totalGB * 100
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	return usedGB, totalGB, percent
}

func isNetworkOrFuseFilesystemType(filesystemType string) bool {
	value := strings.ToLower(strings.TrimSpace(filesystemType))
	if value == "" {
		return false
	}
	if strings.Contains(value, "clouddrive") {
		return true
	}
	switch value {
	case "fuse", "rclone", "sshfs", "s3fs", "gcsfuse", "mergerfs",
		"unionfs", "unionfs-fuse", "nfs", "nfs4", "cifs", "smb", "smb3",
		"smbfs", "afs", "ceph", "cephfs", "glusterfs":
		return true
	}
	if strings.HasPrefix(value, "fuse.") {
		switch strings.TrimPrefix(value, "fuse.") {
		case "rclone", "sshfs", "s3fs", "gcsfuse", "ufs", "mergerfs",
			"unionfs", "unionfs-fuse", "ceph", "ceph-fuse", "cephfs",
			"glusterfs":
			return true
		}
	}
	return false
}

func isNetworkOrFuseCapacityKey(key string) bool {
	value := strings.ToLower(strings.TrimSpace(key))
	if value == "" {
		return false
	}
	if value == "fuse" || strings.HasPrefix(value, "fuse.") ||
		value == "/dev/fuse" || strings.HasSuffix(value, "/fuse") {
		return true
	}
	if value == "rclone" || strings.HasPrefix(value, "rclone:") ||
		strings.Contains(value, "clouddrive") {
		return true
	}
	if strings.HasPrefix(value, "//") {
		return true
	}
	if !strings.HasPrefix(value, "apfs:") &&
		!strings.HasPrefix(value, "overlay:") &&
		!strings.HasPrefix(value, "overlayfs:") &&
		looksLikeRemoteSource(value) {
		return true
	}
	switch value {
	case "sshfs", "s3fs", "gcsfuse", "mergerfs", "unionfs",
		"unionfs-fuse", "ceph", "ceph-fuse", "cephfs", "gluster",
		"glusterfs", "ufs":
		return true
	}
	return false
}

// looksLikeRemoteSource matches host:/export and [ipv6]:/export.
func looksLikeRemoteSource(value string) bool {
	colon := strings.Index(value, ":/")
	if colon <= 0 {
		return false
	}
	host := value[:colon]
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		return true
	}
	if strings.ContainsAny(host, "[]") {
		return false
	}
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}
