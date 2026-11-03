// Bring-up check for the sensor pod's XIAO ESP32-C5: scan both Wi-Fi bands
// and print what it hears, every 10 s, on the USB serial port (115200).
//
// What it proves: the radio and the antenna work on 2.4 AND 5 GHz. With the S1
// powered on in router mode, the house network's band and channel show which
// band the pod should avoid (docs/STATUS.md, "Why the XIAO ESP32-C5").
//
// It only listens - no network is joined and nothing is transmitted beyond
// the probe requests a scan sends. Output contains neighbours' SSIDs and
// BSSIDs: read it, don't commit it.
//
//   arduino-cli compile --fqbn esp32:esp32:XIAO_ESP32C5 firmware/scan
//   arduino-cli upload  --fqbn esp32:esp32:XIAO_ESP32C5 -p <port> firmware/scan
//   arduino-cli monitor -p <port> -c baudrate=115200
#include <WiFi.h>

static const char *band(int ch) { return ch > 14 ? "5G " : "2.4G"; }

void setup() {
  Serial.begin(115200);
  delay(1500);                       // let USB-CDC enumerate before printing
  WiFi.mode(WIFI_STA);
  WiFi.setBandMode(WIFI_BAND_MODE_AUTO);
  Serial.printf("\npod bring-up: %s rev %d, %d core(s), %lu MHz\n",
                ESP.getChipModel(), ESP.getChipRevision(), ESP.getChipCores(),
                (unsigned long)ESP.getCpuFreqMHz());
}

void loop() {
  uint32_t t0 = millis();
  int n = WiFi.scanNetworks(false, true);   // blocking, include hidden
  uint32_t dt = millis() - t0;
  int n24 = 0, n5 = 0;
  Serial.printf("\nscan: %d networks in %lu ms\n", n, (unsigned long)dt);
  Serial.println(" band  ch  rssi  ssid");
  for (int i = 0; i < n; i++) {
    int ch = WiFi.channel(i);
    (ch > 14 ? n5 : n24)++;
    String ssid = WiFi.SSID(i);
    Serial.printf(" %s %3d  %4ld  %s\n", band(ch), ch, (long)WiFi.RSSI(i),
                  ssid.length() ? ssid.c_str() : "(hidden)");
  }
  Serial.printf("summary: 2.4 GHz %d, 5 GHz %d\n", n24, n5);
  WiFi.scanDelete();
  delay(10000);
}
