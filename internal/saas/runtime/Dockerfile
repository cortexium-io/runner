FROM mcr.microsoft.com/playwright@sha256:ee15712262f017b8157421b212b12099146645f44b1bc5c0514cbbe6a229b1d0
WORKDIR /runner
COPY app-base /app
COPY node /usr/local/bin/node
COPY chrome-linux64 /runtime/chrome-linux64
COPY package.json package-lock.json ./
COPY node_modules ./node_modules
COPY browser.mjs container.mjs control.mjs ./
ARG RUNNER_CORTEXIUM_DRIVER_SHA256
ENV RUNNER_CORTEXIUM_DRIVER_SHA256=${RUNNER_CORTEXIUM_DRIVER_SHA256}
LABEL cortexium.runner.cortexium.contract="1" cortexium.runner.cortexium.node="26.13.1" cortexium.runner.cortexium.playwright="1.65.0" cortexium.runner.cortexium.cft="158.0.8127.18" cortexium.runner.cortexium.driver=${RUNNER_CORTEXIUM_DRIVER_SHA256}
USER 1000:1000
ENTRYPOINT ["/usr/local/bin/node", "/runner/container.mjs"]
CMD []
