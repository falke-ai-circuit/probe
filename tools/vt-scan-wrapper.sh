#!/bin/bash
# VT scan wrapper — reads API key from ~/.vt_api_key, runs vt-scan.py
# Usage: ./vt-scan-wrapper.sh <binary_path> [label]

export VT_API_KEY=$(cat ~/.vt_api_key)
cd /opt/data/workspace-operative/hermes-remote/tools
python3 vt-scan.py "$1" "$2"