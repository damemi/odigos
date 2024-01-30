FROM openjdk:8 AS java-builder
RUN apt update -y && apt install -y g++ systemtap-sdt-dev make maven