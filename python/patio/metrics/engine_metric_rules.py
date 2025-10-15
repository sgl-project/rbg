# -*- coding: utf-8 -*-
# @Author: zibai.gj
from typing import Dict

from patio.logger import init_logger
from patio.metrics.standard_rules import StandardRule, RenameStandardRule

logger = init_logger(__name__)

"""
patio provides unified metrics for different engines 
"""
num_requests_running = "patio:num_requests_running"
num_requests_waiting = "patio:num_requests_waiting"
TTFT = "patio:time_to_first_token_seconds"
TPOT = "patio:time_per_output_token_seconds"
ITL = "patio:inter_token_latency_seconds"
e2e_latency = "patio:e2e_request_latency_seconds"
gpu_cache_usage = "patio:gpu_cache_usage_perc"

# vllm metrics: https://docs.vllm.ai/en/latest/design/metrics.html
VLLM_METRIC_STANDARD_RULES: Dict[str, StandardRule] = {
    "vllm:num_requests_running": RenameStandardRule(
        "vllm:num_requests_running", num_requests_running,
    ),
    "vllm:num_requests_waiting": RenameStandardRule(
        "vllm:num_requests_waiting", num_requests_waiting,
    ),
    "vllm:time_to_first_token_seconds": RenameStandardRule(
        "vllm:time_to_first_token_seconds", TTFT,
    ),
    "vllm:time_per_output_token_seconds": RenameStandardRule(
        "vllm:time_per_output_token_seconds", TPOT,
    ),
    "vllm:gpu_cache_usage_perc": RenameStandardRule(
        "vllm:gpu_cache_usage_perc", gpu_cache_usage,
    ),
    "vllm:e2e_request_latency_seconds": RenameStandardRule(
        "vllm:e2e_request_latency_seconds", e2e_latency,
    ),
}

# sglang metrics: https://docs.sglang.ai/references/production_metrics.html
SGLANG_METRIC_STANDARD_RULES = {
    "sglang:num_running_reqs": RenameStandardRule(
        "sglang:num_running_reqs", num_requests_running,
    ),
    "sglang:num_queue_reqs": RenameStandardRule(
        "sglang:num_queue_reqs", num_requests_waiting,
    ),
    "sglang:token_usage": RenameStandardRule(
        "sglang:token_usage", gpu_cache_usage,
    ),
    "sglang:time_to_first_token_seconds": RenameStandardRule(
        "sglang:time_to_first_token_seconds", TTFT,
    ),
    "sglang:inter_token_latency_seconds": RenameStandardRule(
        "sglang:inter_token_latency_seconds", ITL,
    ),
    "sglang:e2e_request_latency_seconds": RenameStandardRule(
        "sglang:e2e_request_latency_seconds", e2e_latency,
    ),
}


def get_metric_standard_rules(engine: str) -> Dict[str, StandardRule]:
    if engine.lower() == "sglang":
        return SGLANG_METRIC_STANDARD_RULES
    elif engine.lower() == "vllm":
        return VLLM_METRIC_STANDARD_RULES
    else:
        raise ValueError(f"Unsupported inference engine: {engine}")
