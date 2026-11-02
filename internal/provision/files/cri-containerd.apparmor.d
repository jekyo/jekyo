# Managed by JEKYO (issue #33).
#
# containerd generates this profile itself, with signal rules bound to the
# bare profile name:
#
#   signal (send,receive) peer=cri-containerd.apparmor.d,
#
# Under AppArmor 4+/5 (Ubuntu 26.04) a container's label is the STACKED
# form "cri-containerd.apparmor.d//&unconfined", which that peer pattern
# does not match — so no process in any container can signal another, not
# even its own child, and anything that stops a helper process (postgres,
# gosu, graceful shutdown in Node or Python) breaks. The peer patterns
# below match the stacked label too.
#
# Everything else mirrors containerd's own template, so replacing this
# profile changes signal mediation and nothing more.
#include <tunables/global>

profile cri-containerd.apparmor.d flags=(attach_disconnected,mediate_deleted) {
  #include <abstractions/base>

  network,
  capability,
  file,
  umount,

  # Host (privileged) processes may send signals to container processes.
  signal (receive) peer=unconfined,
  # The runtime may signal container processes (crictl/kubelet stop).
  signal (receive) peer=cri-containerd.apparmor.d,
  signal (receive) peer=cri-containerd.apparmor.d//&*,
  # Container processes may signal amongst themselves. The trailing
  # patterns are what the generated profile is missing: they cover the
  # stacked label a container actually carries.
  signal (send,receive) peer=cri-containerd.apparmor.d,
  signal (send,receive) peer=cri-containerd.apparmor.d//&*,

  deny @{PROC}/* w,   # deny write for all files directly in /proc (not in a subdir)
  # deny write to files not in /proc/<number>/** or /proc/sys/**
  deny @{PROC}/{[^1-9],[^1-9][^1-9],[^1-9][^1-9][^1-9],[^1-9][^1-9][^1-9][^1-9]*}/** w,
  deny @{PROC}/sys/[^k]** w,  # deny /proc/sys except /proc/sys/k* (effectively /proc/sys/kernel)
  deny @{PROC}/sys/kernel/{?,??,[^s][^h][^m]**} w,  # deny everything except shm* in /proc/sys/kernel/
  deny @{PROC}/sysrq-trigger rwklx,
  deny @{PROC}/mem rwklx,
  deny @{PROC}/kmem rwklx,
  deny @{PROC}/kcore rwklx,

  deny mount,

  deny /sys/[^f]*/** wklx,
  deny /sys/f[^s]*/** wklx,
  deny /sys/fs/[^c]*/** wklx,
  deny /sys/fs/c[^g]*/** wklx,
  deny /sys/fs/cg[^r]*/** wklx,
  deny /sys/firmware/** rwklx,
  deny /sys/kernel/security/** rwklx,

  # suppress ptrace denials when using 'ps' inside a container
  ptrace (trace,read,tracedby,readby) peer=cri-containerd.apparmor.d,
  ptrace (trace,read,tracedby,readby) peer=cri-containerd.apparmor.d//&*,
}
