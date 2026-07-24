#!/usr/bin/env python3
"""VirusTotal scan script — upload binary, poll for results, report.

Usage: python3 vt-scan.py <binary_path> [label]

Tracks submissions in vt-submissions.log for correlation.
"""
import sys
import time
import hashlib
import requests

import os
API_KEY = os.environ.get("VT_API_KEY", "")
if not API_KEY:
    # Fallback: read from file
    try:
        with open(os.path.expanduser("~/.vt_api_key")) as f:
            API_KEY = f.read().strip()
    except FileNotFoundError:
        pass
if not API_KEY:
    print("ERROR: Set VT_API_KEY env var or create ~/.vt_api_key file")
    sys.exit(1)
VT_BASE = "https://www.virustotal.com/api/v3"
HEADERS = {"x-apikey": API_KEY}


def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(8192), b""):
            h.update(chunk)
    return h.hexdigest()


def upload(path):
    with open(path, "rb") as f:
        files = {"file": (path, f)}
        resp = requests.post(f"{VT_BASE}/files", files=files, headers=HEADERS, timeout=120)
    resp.raise_for_status()
    return resp.json()["data"]["id"]


def poll(analysis_id, max_wait=600):
    for i in range(max_wait // 15):
        time.sleep(15)
        resp = requests.get(f"{VT_BASE}/analyses/{analysis_id}", headers=HEADERS, timeout=30)
        if resp.status_code != 200:
            continue
        data = resp.json().get("data", {}).get("attributes", {})
        status = data.get("status", "")
        if status == "completed":
            return data.get("stats", {}), data.get("results", {})
    return None, None


def get_report(file_hash):
    """Get full report by hash (works for already-scanned files)."""
    resp = requests.get(f"{VT_BASE}/files/{file_hash}", headers=HEADERS, timeout=30)
    if resp.status_code == 200:
        attrs = resp.json().get("data", {}).get("attributes", {})
        stats = attrs.get("last_analysis_stats", {})
        results = attrs.get("last_analysis_results", {})
        return stats, results
    return None, None


def main():
    if len(sys.argv) < 2:
        print("Usage: vt-scan.py <binary_path> [label]")
        sys.exit(1)

    path = sys.argv[1]
    label = sys.argv[2] if len(sys.argv) > 2 else path

    file_hash = sha256(path)
    print(f"[VT] File: {path}")
    print(f"[VT] Label: {label}")
    print(f"[VT] SHA256: {file_hash}")

    # Check if already scanned
    stats, results = get_report(file_hash)
    if stats:
        print(f"[VT] Already scanned -- using cached results")
    else:
        print(f"[VT] Uploading...")
        analysis_id = upload(path)
        print(f"[VT] Analysis ID: {analysis_id}")
        print(f"[VT] Polling for results (max 10 min)...")
        stats, results = poll(analysis_id)

    if not stats:
        print(f"[VT] ERROR: No results (timeout or error)")
        sys.exit(1)

    malicious = stats.get("malicious", 0)
    suspicious = stats.get("suspicious", 0)
    undetected = stats.get("undetected", 0)
    total = malicious + suspicious + undetected

    print(f"\n[VT] === Results for '{label}' ===")
    print(f"[VT] Malicious: {malicious}/{total}")
    print(f"[VT] Suspicious: {suspicious}")
    print(f"[VT] Undetected: {undetected}")

    detected = {}
    if results:
        detected = {k: v for k, v in results.items() if v.get("category") in ("malicious", "suspicious")}
        if detected:
            print(f"\n[VT] Detecting engines:")
            for engine, info in sorted(detected.items()):
                print(f"  {engine}: {info.get('result', 'N/A')} ({info.get('category', 'N/A')})")
        else:
            print(f"\n[VT] CLEAN -- 0 detections!")

    # Log submission
    detection_str = "CLEAN"
    if detected:
        parts = []
        for e, r in sorted(detected.items()):
            parts.append(f"{e}:{r.get('result', '?')}")
        detection_str = ", ".join(parts)
    with open("vt-submissions.log", "a") as f:
        f.write(f"{time.strftime('%Y-%m-%d %H:%M')} | {label} | {file_hash} | {malicious}/{total} | {detection_str}\n")

    return 0 if malicious == 0 else 1


if __name__ == "__main__":
    sys.exit(main())