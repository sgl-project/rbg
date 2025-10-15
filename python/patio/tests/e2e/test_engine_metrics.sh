#!/bin/bash

set -e

engine="sglang"
endpoint="http://localhost:8000"
modelName="/models/Qwen3-8B"

# run patio.app and record PID
cd ../../../
INFERENCE_ENGINE_ENDPOINT=$endpoint INFERENCE_ENGINE=$engine python -m patio.app &
patio_pid=$!

# benchmark
total=10
for i in $(seq 1 $total); do
  curl -s -f ${endpoint}/v1/chat/completions -H "Content-Type: application/json" \
  -d '{
        "model": "Qwen/Qwen3-8B",
        "messages": [
          {"role": "user", "content": "Give me a short introduction to large language models."}
        ],
        "temperature": 0.7,
        "top_p": 0.8,
        "top_k": 20,
        "max_tokens": 64,
        "presence_penalty": 1.5,
        "chat_template_kwargs": {"enable_thinking": false}
      }' > /dev/null

  if [ $? -ne 0 ]; then
    echo "ERROR: curl request failed at iteration $i"
    kill $patio_pid
    exit 1
  fi

  percent=$((i * 100 / total))
  echo -ne "\rRunning benchmark... [$i/$total] ${percent}% completed"
done
echo -ne "Benchmark Done!"

# get metrics and check if the TTFT metric exists
metrics=$(curl -s http://localhost:9091/metrics)
echo "$metrics" | grep -q "time_to_first_token_seconds"

if [ $? -ne 0 ]; then
  echo "ERROR: time_to_first_token_seconds metric not found in metrics"
  kill $patio_pid
  exit 1
else
  echo "$metrics" | grep "TYPE patio"
fi

# stop server
kill $patio_pid
