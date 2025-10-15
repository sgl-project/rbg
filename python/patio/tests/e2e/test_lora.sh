#!/bin/bash

engine="sglang"
endpoint="http://localhost:8000"
modelName="/models/Qwen3-8B"

loraName="qwen3-8b-korean-lora"
loraPath="AiStudent1023/qwen3-8b-korean-lora"

# sglang https://docs.sglang.ai/advanced_features/lora.html#Dynamic-LoRA-loading
#python3 -m sglang.launch_server --model-path /models/Qwen3-8B --trust-remote-code \
#            --context-length 2048 --host 0.0.0.0 --port 8000 --enable-metrics \
#            --enable-lora \
#            --cuda-graph-max-bs 2 \
#            --max-loras-per-batch 2 \
#            --max-lora-rank 256 \
#            --lora-target-modules all \
#            --log-level warning


# run patio.app and record PID
cd ../../../
INFERENCE_ENGINE_ENDPOINT=$endpoint INFERENCE_ENGINE=$engine python -m patio.app &
patio_pid=$!


# load lora
curl -X POST http://localhost:9091/load_lora_adapter \
    -H "Content-Type: application/json" \
    -d '{
      "lora_name": "'$loraName'",
      "lora_path": "'"$loraPath"'"
    }'
sleep 5

# unload lora
curl -X POST http://localhost:9091/unload_lora_adapter \
    -H "Content-Type: application/json" \
    -d '{
      "lora_name": "'$loraName'"
    }'

# stop server
kill $patio_pid


