"""Finds connected external drives.

Normally lists /Volumes and asks diskutil about each. With ATLAS_DEV_ROOT set,
every subfolder of that directory is treated as a drive instead, so the whole
tool can be exercised without real disks.
"""
import concurrent.futures
import hashlib
import os
import plistlib
import subprocess


def _diskutil(mount):
    try:
        out = subprocess.run(["diskutil", "info", "-plist", mount], capture_output=True, timeout=15, check=True).stdout
        return plistlib.loads(out)
    except (subprocess.SubprocessError, OSError, plistlib.InvalidFileException):
        return None


def _space(path):
    try:
        st = os.statvfs(path)
        return st.f_blocks * st.f_frsize, st.f_bavail * st.f_frsize
    except OSError:
        return None, None


def facts_for(mount, info):
    cap, free = _space(mount)
    return {
        "name": info.get("VolumeName") or os.path.basename(mount.rstrip("/")),
        "mount": mount,
        "volume_uuid": info.get("VolumeUUID"),
        "filesystem": info.get("FilesystemName") or info.get("FilesystemType"),
        "device": info.get("DeviceIdentifier"),
        "media": info.get("MediaName"),
        "protocol": info.get("BusProtocol"),
        "capacity_bytes": cap,
        "free_bytes": free,
    }


def _dev_volumes(root):
    out = []
    for name in sorted(os.listdir(root)):
        p = os.path.join(root, name)
        if name.startswith(".") or not os.path.isdir(p):
            continue
        uuid = "DEV-" + hashlib.sha1(p.encode()).hexdigest()[:16].upper()
        out.append(facts_for(p, {"VolumeName": name, "VolumeUUID": uuid, "FilesystemName": "dev"}))
    return out


def connected():
    """Facts for every connected external drive, sorted by name."""
    dev = os.environ.get("ATLAS_DEV_ROOT")
    if dev:
        return _dev_volumes(dev)
    try:
        names = os.listdir("/Volumes")
    except OSError:
        return []
    mounts = [os.path.join("/Volumes", n) for n in names if not os.path.islink(os.path.join("/Volumes", n))]
    allow_dmg = bool(os.environ.get("ATLAS_ALLOW_DMG"))
    out = []
    with concurrent.futures.ThreadPoolExecutor(8) as pool:
        for mount, info in zip(mounts, pool.map(_diskutil, mounts)):
            if not info or not info.get("VolumeUUID") or info.get("Internal"):
                continue
            if info.get("BusProtocol") == "Disk Image" and not allow_dmg:
                continue
            out.append(facts_for(mount, info))
    return sorted(out, key=lambda f: f["name"].lower())
