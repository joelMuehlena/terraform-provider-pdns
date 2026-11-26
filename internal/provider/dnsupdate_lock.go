package provider

import "context"

// dnsupdateLockKey is kept in TSIG-ALLOW-DNSUPDATE of zones with
// lock_dnsupdate = true. PowerDNS accepts unsigned updates (from
// allow-dnsupdate-from) while that metadata is empty, but refuses every update
// not signed by a listed key once it lists anything. No TSIG key may have this
// name (enforced by pdns_tsig_key), so it never authorizes an update itself;
// keys attached with pdns_zone_tsig_key are listed next to it.
const dnsupdateLockKey = "pdns-provider-dnsupdate-locked."

// hasDNSUpdateLock reports whether the zone's TSIG-ALLOW-DNSUPDATE lists the
// lock placeholder.
func (d *PDNSProviderData) hasDNSUpdateLock(ctx context.Context, zone string) (bool, error) {
	names, err := d.pdnsClient.GetZoneMetadata(ctx, zone, metadataTSIGAllowDNSUpdate)
	if err != nil {
		return false, err
	}
	return containsDNSName(names, dnsupdateLockKey), nil
}

// setDNSUpdateLock adds or removes the lock placeholder, leaving the keys of
// pdns_zone_tsig_key attachments untouched. Like the attachments it verifies
// the result and retries, so a concurrent writer that rewrites the list from a
// stale read (e.g. another run removing the last attachment) cannot silently
// drop the placeholder.
func (d *PDNSProviderData) setDNSUpdateLock(ctx context.Context, zone string, lock bool) error {
	unlock := d.lockZone(zone)
	defer unlock()

	return retryWrites(ctx, d.lockRetry(), "TSIG-ALLOW-DNSUPDATE of zone "+zone,
		func(ctx context.Context) (bool, error) {
			locked, err := d.hasDNSUpdateLock(ctx, zone)
			return locked == lock, err
		},
		func(ctx context.Context) error {
			names, err := d.pdnsClient.GetZoneMetadata(ctx, zone, metadataTSIGAllowDNSUpdate)
			if err != nil {
				return err
			}
			return d.pdnsClient.SetZoneMetadata(ctx, zone, metadataTSIGAllowDNSUpdate, setDNSName(names, dnsupdateLockKey, lock))
		},
	)
}

// lockRetry returns the retry settings for setDNSUpdateLock.
func (d *PDNSProviderData) lockRetry() applyRetry {
	if d.lockRetryConfig != nil {
		return *d.lockRetryConfig
	}
	return defaultApplyRetry
}
