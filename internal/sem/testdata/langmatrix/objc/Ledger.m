#import <Foundation/Foundation.h>

@interface Ledger : NSObject
@property (nonatomic) NSInteger total;
- (NSInteger)add:(NSInteger)amount;
@end

@implementation Ledger

- (NSInteger)add:(NSInteger)amount {
    return self.total + amount;
}

@end

NSInteger LedgerDouble(NSInteger amount) {
    Ledger *ledger = [[Ledger alloc] init];
    return [ledger add:amount] * 2;
}
