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
// pdns_zone_tsig_key attachments untouched.
func (d *PDNSProviderData) setDNSUpdateLock(ctx context.Context, zone string, lock bool) error {
	unlock := d.lockZone(zone)
	defer unlock()

	names, err := d.pdnsClient.GetZoneMetadata(ctx, zone, metadataTSIGAllowDNSUpdate)
	if err != nil {
		return err
	}
	if containsDNSName(names, dnsupdateLockKey) == lock {
		return nil
	}
	return d.pdnsClient.SetZoneMetadata(ctx, zone, metadataTSIGAllowDNSUpdate, setDNSName(names, dnsupdateLockKey, lock))
}
