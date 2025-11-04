# -*- coding: utf-8 -*-
# @Author: zibai.gj
from patio.topo.client.base_topo_client import GroupTopoClient
from patio.topo.client.sgl_topo_client import SGLangGroupTopoClient
from patio.topo.server.base_topo_server import GroupTopoServer
from patio.topo.server.sgl_topo_server import SGLangGroupTopoServer


def create_topo_client(topo_type: str, worker_info: dict) -> GroupTopoClient:
    if topo_type.lower() == "sglang":
        return SGLangGroupTopoClient(worker_info)
    else:
        raise ValueError(f"Invalid topo type: {topo_type}")


def create_topo_server(topo_type: str) -> GroupTopoServer:
    if topo_type.lower() == "sglang":
        return SGLangGroupTopoServer()
    else:
        raise ValueError(f"Invalid topo type: {topo_type}")
