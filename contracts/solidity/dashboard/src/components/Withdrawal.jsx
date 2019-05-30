import React, { Component } from 'react'
import { Button } from 'react-bootstrap'
import WithWeb3Context from './WithWeb3Context'

class Withdrawal extends Component {

  finishUnstake = async () => {
    const { web3, withdrawal } = this.props
    web3.stakingContract.methods.finishUnstake(withdrawal.id, {from: web3.yourAddress, gas: 110000})
  }

  render() {
    const { withdrawal } = this.props
    let action = 'N/A'
    if (withdrawal.available) {
      action = <Button bsSize="small" bsStyle="primary" onClick={this.finishUnstake}>Finish Unstake</Button>
    }

    return (
      <tr>
        <td>{withdrawal.amount}</td>
        <td className="text-mute">{withdrawal.availableAt}</td>
        <td>{action}</td>
      </tr>
    )
  }
}

export default WithWeb3Context(Withdrawal)
