FROM python:3.12-slim
WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY fileserver.py incidents.py ci_mock.py ollama_shim.py gate_proxy.py ./
USER 65532:65532
