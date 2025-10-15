# -*- coding: utf-8 -*-
# @Author: zibai.gj
import threading

from patio.topo.server.base_topo_server import GroupTopoServer


class SGLangGroupTopoServer(GroupTopoServer):
    _instance = None

    # Singleton
    def __new__(cls, *args, **kwargs):
        if cls._instance is None:
            cls._instance = super(GroupTopoServer, cls).__new__(cls, *args, **kwargs)
            cls._instance.__initialized = False
        return cls._instance

    def __init__(self):
        if not self.__initialized:
            super().__init__()
            self.workers = {}
            self.sync_thread_running = False
            self.lock = threading.Lock()
            self.__initialized = True

    def register(self, worker_info: dict):
        raise NotImplementedError

    def unregister(self, endpoint: str):
        raise NotImplementedError

    def worker_health_check(self):
        raise NotImplementedError
