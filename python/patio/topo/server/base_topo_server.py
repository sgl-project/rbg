# -*- coding: utf-8 -*-
# @Author: zibai.gj

from abc import ABC, abstractmethod

from patio.logger import init_logger

logger = init_logger(__name__)


class GroupTopoServer(ABC):

    def __init__(self):
        self.worker_heartbeats = {}

    @abstractmethod
    def unregister(self, endpoint: str):
        raise NotImplementedError

    @abstractmethod
    def register(self, data: str):
        raise NotImplementedError

    @abstractmethod
    def worker_health_check(self):
        raise NotImplementedError

    def get_info(self) -> str:
        return ",".join(str(key) for key in self.worker_heartbeats.keys())
