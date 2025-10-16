# -*- coding: utf-8 -*-
# @Author: zibai.gj
from typing import Dict

import requests
from prometheus_client.parser import text_string_to_metric_families
from prometheus_client.registry import Collector

from patio.logger import init_logger
from patio.metrics.standard_rules import StandardRule

logger = init_logger(__name__)

"""
For a single-process deployment (for example running a single uvicorn process), 
do not need MultiProcessCollector — the single CollectorRegistry in the previous example is sufficient.

For a multi-process deployment (common when using gunicorn with multiple workers),
if you want to aggregate metrics from all workers (rather than only seeing one worker’s metrics), 
need to enable prometheus_client’s multiprocess mode and 
use MultiProcessCollector at the /metrics endpoint to combine the metrics files produced by each process.
"""


class EngineCollector(Collector):
    """
    Collector for Prometheus metrics from an inference engine.
    """

    def __init__(self, scrape_endpoint: str, metric_rules: Dict[str, StandardRule], prefix: str = "",
                 timeout: float = 2.0):
        self.scrape_endpoint = scrape_endpoint
        self.prefix = prefix
        self.timeout = timeout
        self.metric_rules = metric_rules

        self.session = requests.Session()

    def _collector(self):
        try:
            response = self.session.get(self.scrape_endpoint, timeout=self.timeout)
            if response.status_code != 200:
                logger.warning(
                    f"Failed to collect metrics from {self.scrape_endpoint}"
                    f"with status code {response.status_code},"
                    f"response: {response.text}"
                )
                return ""
            return response.text
        except Exception as e:
            logger.warning(
                f"Failed to collect metrics from {self.scrape_endpoint} with error: {e}"
            )
            return ""

    def collect(self):
        metrics_text = self._collector()

        for m in text_string_to_metric_families(metrics_text):
            if m.name in self.metric_rules:
                new_metric = self.metric_rules[m.name](m)
                if new_metric is not None:
                    yield from new_metric
