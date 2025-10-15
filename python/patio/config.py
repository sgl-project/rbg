# -*- coding: utf-8 -*-
# @Author: zibai.gj
# @Time  : 2025-03-07
import threading
from tarfile import SUPPORTED_TYPES

EXIT_EVENT = threading.Event()

DEFAULT_METRIC_COLLECTOR_TIMEOUT = 1

EXCLUDE_ENDPOINTS = ["/metrics"]

DEFAULT_PATIO_HOST = "0.0.0.0"

DEFAULT_PATIO_PORT = 9091

# Group Topo
Mooncake = "Mooncake"

SUPPORTED_TOPO_TYPES = [Mooncake]
