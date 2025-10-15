# -*- coding: utf-8 -*-
# @Author: zibai.gj
from prometheus_client import Histogram, Counter, CollectorRegistry

patio_registry = CollectorRegistry()

# Prometheus metrics
REQUEST_COUNT = Counter(
    "http_requests_total",
    "Total HTTP requests",
    ["method", "endpoint", "status"],
    registry=patio_registry,
)

REQUEST_LATENCY = Histogram(
    "http_request_latency_seconds",
    "HTTP request latency in seconds",
    ["method", "endpoint"],
    registry=patio_registry,
)
