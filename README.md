# Storage Calculator

A replacement for the Lagoon storage-calculator service that runs in remote clusters

Individual namespaces can have the following labels to change the behaviour of storage calculator

* `lagoon.sh/storageCalculatorEnabled` if set to false, will exclude it from having its volumes calculated
* `lagoon.sh/storageCalculatorIgnoreRegex` this will override the default regex (if one is set)

Storage-calculator pods created by this controller get a label of `lagoon.sh/storageCalculator=true`

Storage-calculator connects to rabbitmq in lagoon-core and publishes to the actions-handler `lagoon-actions` exchange for the actions-handler in core to process.

```
#example payload
{
	"type": "updateEnvironmentStorage",
	"eventType": "environmentStorage",
	"data": {
		"claims": [{
			"environment": 1,
			"persistentStorageClaim": "nginx",
			"bytesUsed": 1200
		}, {
			"environment": 1,
			"persistentStorageClaim": "solr",
			"bytesUsed": 2200
		}, {
			"environment": 1,
			"persistentStorageClaim": "mariadb",
			"bytesUsed": 3200
		}]
	}
}
```