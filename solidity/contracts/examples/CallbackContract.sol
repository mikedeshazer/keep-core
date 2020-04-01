pragma solidity ^0.5.4;


/**
 * @title CallbackContract
 * @dev Example callback contract for Random Beacon.
 */
contract CallbackContract {

    uint256 internal _lastEntry;

    function __beaconCallback(uint256 entry)
        public
    {
        _lastEntry = entry;
    }

    function lastEntry() public view returns (uint256)
    {
        return _lastEntry;
    }
}
