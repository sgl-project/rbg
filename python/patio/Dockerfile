FROM registry-cn-hangzhou.ack.aliyuncs.com/dev/python:3.12-alpine3.21
WORKDIR /app
COPY python/patio /app/patio
RUN pip install -r /app/patio/requirements.txt --no-cache-dir -i https://mirrors.aliyun.com/pypi/simple/ --trusted-host mirrors.aliyun.com
ENV PYTHONPATH="$PYTHONPATH:/app/patio"
ENTRYPOINT ["python3", "-m", "patio.app"]

# docker build -t patio-runtime:v1 .