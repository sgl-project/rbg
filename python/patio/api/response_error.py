# -*- coding: utf-8 -*-
# @Author: zibai.gj
from http import HTTPStatus

from patio.api.protocol import ErrorResponse


def invalid_error(message: str, code: int = 400, err_type: str = "InvalidError"):
    return ErrorResponse(message=message, type=err_type, code=code)


def not_implemented_error(message: str, code: int = HTTPStatus.NOT_IMPLEMENTED, err_type: str = "NotImplementedError"):
    return ErrorResponse(message=message, type=err_type, code=code)


def server_error(message: str, code: int = HTTPStatus.INTERNAL_SERVER_ERROR, err_type: str = "ServerError"):
    return ErrorResponse(message=message, type=err_type, code=code)
