#!/bin/bash
echo "flushdb" | redis-cli -h 127.0.0.1 -p 6379 -n 5
nohup ./frilstart >/dev/null 2>&1 &