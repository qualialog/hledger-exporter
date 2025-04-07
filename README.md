# prometheus metrics for hledger

![dashboard example](/assets/screenshot.png)

this is a very opinionated prometheus exporter for hledger.

It's useful for my frankestein hledger journal file, but I highly suggest you fork it and make it your own.

## hledger journal file

It assumes you'll provision a Gitea token + the raw url to the file.
See `fetchJournal` function if you want to change how you provision it.

## grafana dash

import `grafana-dashboard.json` and it should work out of the box with this metrics.

## prometheus scrape job

It assumes ledger container is on the same network as Prometheus.
Also, if Prometheus is not the default datasource, you'll need to update each panel manually

```
  - job_name: 'ledger_exporter'
    static_configs:
      - targets: ['ledger:9000']
```

Inspired by [this blog post](https://memo.barrucadu.co.uk/personal-finance.html) in Barrucadu's memos
