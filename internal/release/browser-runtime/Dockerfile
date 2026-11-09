FROM mcr.microsoft.com/playwright@sha256:ee15712262f017b8157421b212b12099146645f44b1bc5c0514cbbe6a229b1d0
WORKDIR /runner
ENV PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 PLAYWRIGHT_BROWSERS_PATH=/ms-playwright
COPY package.json package-lock.json ./
RUN npm ci --ignore-scripts --omit=dev --no-audit --no-fund
COPY --chmod=0444 browser.mjs browser_render.mjs browser_contract.mjs browser_container.mjs browser_fixture.mjs ./
ARG RUNNER_BROWSER_DRIVER_SHA256
ENV RUNNER_BROWSER_DRIVER_SHA256=${RUNNER_BROWSER_DRIVER_SHA256}
LABEL cortexium.runner.browser.contract="2" cortexium.runner.browser.playwright="1.64.1" cortexium.runner.browser.driver=${RUNNER_BROWSER_DRIVER_SHA256}
USER 1000:1000
ENTRYPOINT ["node", "/runner/browser_container.mjs"]
CMD []
