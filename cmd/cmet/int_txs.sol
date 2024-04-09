// SPDX-License-Identifier: MIT
pragma solidity ^0.8.0;

// On geth console
// loadScript('int_txs.js')
// var wallet = offlineWalletOpen('./dude-1', 'password')
// var tx = { from: wallet.address, data: MultiSender_data, gas: 10000000,
//            gasPrice: eth.gasPrice, nonce: eth.getTransactionCount(wallet.address) }
// var stx = offlineWalletSignTx(wallet.id, tx, eth.chainId())
// var contractHash = eth.sendRawTransaction(stx)
// // wait for the transaction to be mined
// var contractAddr = eth.getTransactionReceipt(contractHash).contractAddress
// var multiSender = new web3.eth.contract(MultiSender_contract.abi).at(contractAddr)
//
// var data = multiSender.multiSend.getData([to1, to2, to3], [amount1, amount2, amount3], 10)
// var tx = { from: wallet.address, to: multiSender.address, data: data,
//            gas: 10000000, gasPrice: eth.gasPrice, nonce: eth.getTransactionCount(wallet.address),
//            value: 1000000 }
// var stx = offlineWalletSignTx(wallet.id, tx, eth.chainId())
// var th = eth.sendRawTransaction(stx)
// eth.getTransactionReceipt(th)

// With ethers
// const { ethers } = require('ethers')
// eval('var contract_data=' + loadFile('./int_txs.json'))
// var url = 'http://localhost:8588'
// var conn = new ethers.providers.JsonRpcProvider(url)
// var wallet = await ethers.Wallet.fromEncryptedJson(loadFile('dude-1'), 'password')
// var tx = { from: wallet.address, data: contract_data.bytecode, gasLimit: 10000000,
//            gasPrice: await conn.getGasPrice(), nonce: conn.getTransactionCount(wallet.address),
//            chainId: (await conn.getNetwork()).chainId}
// var stx = await wallet.signTransaction(tx)
// var rcpt = await (await conn.sendTransaction(stx)).wait()
// var contractAddress = rcpt.contractAddress
// var multiSender = new ethers.Contract(
//     contractAddress, contract_data.abi, conn)
// var data = multiSender.interface.encodeFunctionData("multiSend",
//     [[to1, to2, to3], [amount1, amount2, amount3], 10])
// var tx = { from: wallet.address, to: multiSender.address, data: data,
//            gasLimit: 10000000, gasPrice: await conn.getGasPrice(),
//            nonce: conn.getTransactionCount(wallet.address),
//            value: 1000000,
//            chainId: (await conn.getNetwork()).chainId}
// var stx = await wallet.signTransaction(tx)
// var rcpt = await conn.sendTransaction()

contract MultiSender {
    function simpleSend(address _to, uint256 _amount) public payable {
        payable(_to).transfer(_amount);

        uint256 remainingBalance = address(this).balance;
        if (remainingBalance > 0) {
            payable(msg.sender).transfer(remainingBalance);
        }
    }

    function multiSend(address[] calldata _tos, uint256[] calldata _amounts, uint256 _count) public payable {
        require(_tos.length == _amounts.length, "lengths don't match");
        for (uint256 i = 0; i < _count; i++) {
            for (uint256 j = 0; j < _tos.length; j++) {
                payable(_tos[j]).transfer(_amounts[j]);
            }
        }

        uint256 remainingBalance = address(this).balance;
        if (remainingBalance > 0) {
            payable(msg.sender).transfer(remainingBalance);
        }
    }
}

// EOF
