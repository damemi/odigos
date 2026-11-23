FROM eclipse-temurin:17 AS java-builder
RUN apt update -y && apt install -y g++ systemtap-sdt-dev make maven gradle
